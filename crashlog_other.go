//go:build !windows

package main

import "os"

// redirectStderr tees stderr to the log file. On non-Windows the standard error
// stream is the usual file descriptor, so reassigning os.Stderr is enough for
// app-level writes; runtime fault output keeps its original destination.
func redirectStderr(f *os.File) {
	os.Stderr = f
}
