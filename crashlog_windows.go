package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// redirectStderr points the process's standard-error handle at the log file so
// that crash output written to stderr — which a windowless (no console) GUI
// build would otherwise discard — is appended to parley.log. This is
// best-effort: the Go runtime caches its stderr handle at startup, so a raw
// runtime fault may still bypass this, but Go-level panics (captured by
// logPanic) and anything written via os.Stderr/log land in the file.
func redirectStderr(f *os.File) {
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
	os.Stderr = f
}
