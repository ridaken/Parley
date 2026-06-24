package main

import (
	"os"
	"unsafe"

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

var (
	modkernel32               = windows.NewLazySystemDLL("kernel32.dll")
	moduser32                 = windows.NewLazySystemDLL("user32.dll")
	procGetConsoleProcessList = modkernel32.NewProc("GetConsoleProcessList")
	procGetConsoleWindow      = modkernel32.NewProc("GetConsoleWindow")
	procFreeConsole           = modkernel32.NewProc("FreeConsole")
	procShowWindow            = moduser32.NewProc("ShowWindow")
)

const swHide = 0 // SW_HIDE

// hideOwnedConsole hides + detaches the console window, but ONLY when this
// process is the sole process attached to it — i.e. Windows allocated the
// console for us (a console-subsystem build, or a double-click launch). If we
// were started from an existing terminal (more than one process attached), the
// console belongs to the user's shell and is left untouched, so we never close
// the window they launched us from. A GUI-subsystem build has no console at all,
// in which case this is a no-op. This is the runtime safeguard that keeps a
// stray terminal window from ever lingering at launch.
func hideOwnedConsole() {
	var pids [8]uint32
	r, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&pids[0])),
		uintptr(len(pids)),
	)
	count := uint32(r)
	if count != 1 {
		// 0 = no console (GUI build); >1 = launched from an existing shell.
		return
	}
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
		_, _, _ = procShowWindow.Call(hwnd, uintptr(swHide))
	}
	_, _, _ = procFreeConsole.Call()
}
