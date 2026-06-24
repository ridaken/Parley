package store

import (
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestMain lets the store tests run on a headless CI runner that has no OS
// keychain (Secret Service / Credential Manager). When PARLEY_KEYRING_MOCK is
// set, keyring calls use go-keyring's in-memory provider; locally the env var is
// unset, so the tests exercise the real OS keychain as before.
func TestMain(m *testing.M) {
	if os.Getenv("PARLEY_KEYRING_MOCK") != "" {
		keyring.MockInit()
	}
	os.Exit(m.Run())
}
