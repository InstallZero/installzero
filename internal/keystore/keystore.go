// Package keystore keeps hosted-model API keys in the operating system's
// credential store: Windows Credential Manager, the macOS Keychain, or a
// Secret Service keyring (GNOME Keyring, KWallet) on Linux. Keys are stored
// per user under "installzero/<provider>" and never written to files.
package keystore

import "errors"

var (
	// ErrNotFound: no key is stored for the provider.
	ErrNotFound = errors.New("no key stored")
	// ErrUnavailable: this system has no credential store installzero can
	// use (no secret-tool, or no keyring running).
	ErrUnavailable = errors.New("no credential store available")
)

// service is the name entries are stored under.
const service = "installzero"

func target(provider string) string { return service + "/" + provider }

// Name is the store's name for messages, with where a provider's key is.
func Name(provider string) string { return storeName + " (" + target(provider) + ")" }

// Get returns the key stored for a provider.
func Get(provider string) (string, error) { return get(provider) }

// Set stores a provider's key, replacing any stored one.
func Set(provider, key string) error { return set(provider, key) }

// Delete removes a provider's key; deleting a missing one is ErrNotFound.
func Delete(provider string) error { return del(provider) }
