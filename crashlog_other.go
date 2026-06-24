//go:build !windows

package main

import "os"

// redirectStderr tees stderr to the log file. On non-Windows the standard error
// stream is the usual file descriptor, so reassigning os.Stderr is enough for
// app-level writes; runtime fault output keeps its original destination.
func redirectStderr(f *os.File) {
	os.Stderr = f
}

// hideOwnedConsole is a no-op off Windows (the stray-console problem is
// Windows-specific). Defined so main.go can call it unconditionally.
func hideOwnedConsole() {}
