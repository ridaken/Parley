// Package stt manages local transcription servers and transcription requests.
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

// Server supervises a local transcription subprocess.
type Server struct {
	name           string
	binPath        string
	args           []string
	requiredPaths  []string
	host           string
	port           int
	logPath        string
	startupTimeout time.Duration
	cmd            *exec.Cmd
	logFile        *os.File
	done           chan error
}

// HasNVIDIAGPU reports whether Windows can see at least one working NVIDIA GPU.
// Nemotron still gets a guarded startup attempt and CPU Whisper fallback; this
// inexpensive probe avoids launching it on machines where it cannot work.
func HasNVIDIAGPU() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	cmd := exec.Command("nvidia-smi", "-L")
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	return err == nil && strings.Contains(strings.ToLower(string(out)), "gpu ")
}

// NewServer configures (but does not start) a whisper.cpp server. If logPath is
// non-empty, the server's stdout/stderr are written there; otherwise they are
// discarded. Importantly, the child is never given the parent's console handles,
// which (together with CREATE_NO_WINDOW on Windows) keeps it fully windowless.
func NewServer(binPath, modelPath, host string, port int, logPath string) *Server {
	return &Server{
		name:          "whisper.cpp",
		binPath:       binPath,
		args:          []string{"-m", modelPath, "--host", host, "--port", strconv.Itoa(port)},
		requiredPaths: []string{binPath, modelPath},
		host:          host,
		port:          port,
		logPath:       logPath,
	}
}

// NewCommandServer configures a transcription server with an arbitrary command.
// It is used by the optional Nemotron Python sidecar while keeping the same
// lifecycle and health-check behavior as the bundled whisper.cpp process.
func NewCommandServer(name, binPath string, args, requiredPaths []string, host string, port int, logPath string, startupTimeout time.Duration) *Server {
	return &Server{
		name:           name,
		binPath:        binPath,
		args:           append([]string(nil), args...),
		requiredPaths:  append([]string(nil), requiredPaths...),
		host:           host,
		port:           port,
		logPath:        logPath,
		startupTimeout: startupTimeout,
	}
}

// URL is the base HTTP URL of the server.
func (s *Server) URL() string {
	return "http://" + s.host + ":" + strconv.Itoa(s.port)
}

// Start spawns the subprocess and blocks until it answers HTTP or ctx is done.
func (s *Server) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, path := range s.requiredPaths {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s required file not found at %s: %w", s.name, path, err)
		}
	}

	s.cmd = exec.Command(s.binPath, s.args...)
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
		return fmt.Errorf("start %s server: %w", s.name, err)
	}
	s.done = make(chan error, 1)
	go func() {
		s.done <- s.cmd.Wait()
		close(s.done)
	}()

	return s.waitReady(ctx)
}

func (s *Server) waitReady(ctx context.Context) error {
	timeout := s.startupTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
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
				return fmt.Errorf("%s server exited before becoming ready", s.name)
			}
			return fmt.Errorf("%s server exited before becoming ready: %w", s.name, err)
		default:
		}
		resp, err := client.Get(s.URL() + "/")
		if err == nil {
			resp.Body.Close()
			return nil // any HTTP response means the server is listening
		}
		if time.Now().After(deadline) {
			s.Stop()
			return fmt.Errorf("%s server did not become ready within %s", s.name, timeout)
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
