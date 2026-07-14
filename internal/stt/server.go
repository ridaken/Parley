// Package stt manages the bundled whisper.cpp server and transcription requests.
package stt

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Server supervises a whisper-server.exe subprocess.
type Server struct {
	binPath   string
	modelPath string
	host      string
	port      int
	logPath   string
	cmd       *exec.Cmd
	logFile   *os.File
	done      chan error
}

// HasNVIDIAGPU reports whether Windows can see at least one working NVIDIA GPU.
// The CUDA whisper build still gets a guarded startup attempt and CPU fallback;
// this probe avoids launching it on machines where it cannot possibly work.
func HasNVIDIAGPU() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	cmd := exec.Command("nvidia-smi", "-L")
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	return err == nil && strings.Contains(strings.ToLower(string(out)), "gpu ")
}

// NewServer configures (but does not start) a whisper server. If logPath is
// non-empty, the server's stdout/stderr are written there; otherwise they are
// discarded. Importantly, the child is never given the parent's console handles,
// which (together with CREATE_NO_WINDOW on Windows) keeps it fully windowless.
func NewServer(binPath, modelPath, host string, port int, logPath string) *Server {
	return &Server{binPath: binPath, modelPath: modelPath, host: host, port: port, logPath: logPath}
}

// URL is the base HTTP URL of the server.
func (s *Server) URL() string {
	return "http://" + s.host + ":" + strconv.Itoa(s.port)
}

// Start spawns the subprocess and blocks until it answers HTTP or ctx is done.
func (s *Server) Start(ctx context.Context) error {
	if _, err := os.Stat(s.binPath); err != nil {
		return fmt.Errorf("whisper server binary not found at %s: %w", s.binPath, err)
	}
	if _, err := os.Stat(s.modelPath); err != nil {
		return fmt.Errorf("whisper model not found at %s: %w", s.modelPath, err)
	}

	s.cmd = exec.Command(s.binPath,
		"-m", s.modelPath,
		"--host", s.host,
		"--port", strconv.Itoa(s.port),
	)
	// Route output to a log file (or discard) — never the parent's console
	// handles, which would otherwise keep a console window alive.
	if s.logPath != "" {
		if f, err := os.Create(s.logPath); err == nil {
			s.logFile = f
			s.cmd.Stdout = f
			s.cmd.Stderr = f
		}
	}
	hideWindow(s.cmd) // CREATE_NO_WINDOW on Windows
	if err := s.cmd.Start(); err != nil {
		s.closeLog()
		return fmt.Errorf("start whisper server: %w", err)
	}
	s.done = make(chan error, 1)
	go func() {
		s.done <- s.cmd.Wait()
		close(s.done)
	}()

	return s.waitReady(ctx)
}

func (s *Server) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(60 * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-ctx.Done():
			s.Stop()
			return ctx.Err()
		case err := <-s.done:
			s.cmd = nil
			s.closeLog()
			if err == nil {
				return fmt.Errorf("whisper server exited before becoming ready")
			}
			return fmt.Errorf("whisper server exited before becoming ready: %w", err)
		default:
		}
		resp, err := client.Get(s.URL() + "/")
		if err == nil {
			resp.Body.Close()
			return nil // any HTTP response means the server is listening
		}
		if time.Now().After(deadline) {
			s.Stop()
			return fmt.Errorf("whisper server did not become ready within timeout")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// Stop terminates the subprocess if running.
func (s *Server) Stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		if s.done != nil {
			<-s.done
		}
		s.cmd = nil
	}
	s.done = nil
	s.closeLog()
}

func (s *Server) closeLog() {
	if s.logFile != nil {
		_ = s.logFile.Close()
		s.logFile = nil
	}
}
