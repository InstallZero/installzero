package keystore

import (
	"errors"
	"runtime"
	"testing"
)

// The round trip writes to the real store, under a provider name nothing
// else uses; it runs where the store is always there (Windows).
func TestRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Keychain and Secret Service need a signed-in desktop session")
	}
	const p = "installzero-test"
	Delete(p)
	if _, err := Get(p); !errors.Is(err, ErrNotFound) {
		t.Fatalf("before: %v", err)
	}
	for _, key := range []string{"sk-test-0123456789", "änderung-ключ-🔑"} {
		if err := Set(p, key); err != nil {
			t.Fatal(err)
		}
		got, err := Get(p)
		if err != nil || got != key {
			t.Fatalf("got %q, %v; want %q", got, err, key)
		}
	}
	if err := Delete(p); err != nil {
		t.Fatal(err)
	}
	if err := Delete(p); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
