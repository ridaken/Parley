//go:build windows

package stt

import (
	"os/exec"
	"syscall"
)

// createNoWindow prevents Windows from allocating a console window for a
// console-subsystem child process (whisper-server.exe).
const createNoWindow = 0x08000000 // CREATE_NO_WINDOW

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
