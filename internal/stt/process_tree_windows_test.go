//go:build windows

package stt

import (
	"os/exec"
	"testing"
	"time"
)

func TestSuperviseProcessTreeKillsProcessWhenJobCloses(t *testing.T) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", "Start-Sleep -Seconds 30")
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	release, err := superviseProcessTree(cmd.Process)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("superviseProcessTree: %v", err)
	}
	release()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("closing the process job did not terminate its process")
	}
}
