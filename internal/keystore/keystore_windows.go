package keystore

import (
	"errors"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const storeName = "Windows Credential Manager"

var (
	advapi32   = syscall.NewLazyDLL("advapi32.dll")
	credWrite  = advapi32.NewProc("CredWriteW")
	credRead   = advapi32.NewProc("CredReadW")
	credDelete = advapi32.NewProc("CredDeleteW")
	credFree   = advapi32.NewProc("CredFree")
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2 // this user, on this PC, across sign-ins
	errorNotFound           = syscall.Errno(1168)
	errorNoSuchLogonSession = syscall.Errno(1312)
	maxCredentialBlobSize   = 5 * 512
)

// credential is CREDENTIALW.
type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, errorNotFound):
		return ErrNotFound
	case errors.Is(err, errorNoSuchLogonSession):
		return ErrUnavailable
	}
	return err
}

func get(provider string) (string, error) {
	name, err := syscall.UTF16PtrFromString(target(provider))
	if err != nil {
		return "", err
	}
	var c *credential
	if r, _, e := credRead.Call(uintptr(unsafe.Pointer(name)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&c))); r == 0 {
		return "", mapErr(e)
	}
	defer credFree.Call(uintptr(unsafe.Pointer(c)))
	blob := unsafe.Slice(c.CredentialBlob, c.CredentialBlobSize)
	u := make([]uint16, len(blob)/2)
	for i := range u {
		u[i] = uint16(blob[2*i]) | uint16(blob[2*i+1])<<8
	}
	return string(utf16.Decode(u)), nil
}

func set(provider, key string) error {
	name, err := syscall.UTF16PtrFromString(target(provider))
	if err != nil {
		return err
	}
	user, _ := syscall.UTF16PtrFromString(provider)
	comment, _ := syscall.UTF16PtrFromString("InstallZero: " + provider + " API key")
	u := utf16.Encode([]rune(key))
	blob := make([]byte, 2*len(u))
	for i, x := range u {
		blob[2*i], blob[2*i+1] = byte(x), byte(x>>8)
	}
	if len(blob) == 0 || len(blob) > maxCredentialBlobSize {
		return errors.New("the key is empty or too long for Credential Manager")
	}
	c := credential{
		Type: credTypeGeneric, TargetName: name, Comment: comment, UserName: user,
		CredentialBlobSize: uint32(len(blob)), CredentialBlob: &blob[0], Persist: credPersistLocalMachine,
	}
	if r, _, e := credWrite.Call(uintptr(unsafe.Pointer(&c)), 0); r == 0 {
		return mapErr(e)
	}
	return nil
}

func del(provider string) error {
	name, err := syscall.UTF16PtrFromString(target(provider))
	if err != nil {
		return err
	}
	if r, _, e := credDelete.Call(uintptr(unsafe.Pointer(name)), credTypeGeneric, 0); r == 0 {
		return mapErr(e)
	}
	return nil
}
