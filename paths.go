package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// resolveResource locates a bundled resource (whisper binary/model) by checking
// a few base directories: the working dir, the executable's dir, and parents of
// the executable's dir (so it works both via `task dev` and a packaged build).
func resolveResource(rel string) (string, error) {
	var bases []string
	if wd, err := os.Getwd(); err == nil {
		bases = append(bases, wd)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		bases = append(bases, dir, filepath.Dir(dir), filepath.Dir(filepath.Dir(dir)))
	}
	for _, b := range bases {
		p := filepath.Join(b, rel)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("could not locate %q (searched %v)", rel, bases)
}

// dataDir is the per-user app data directory.
func dataDir() string {
	if cfg, err := os.UserConfigDir(); err == nil {
		return filepath.Join(cfg, "Parley")
	}
	return "."
}

// recordingsDir is where session audio is stored.
func recordingsDir() string {
	return filepath.Join(dataDir(), "recordings")
}

// dbPath is the SQLite database location (its directory is created if needed).
func dbPath() string {
	dir := dataDir()
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "parley.db")
}

// logPath is the application log file (diagnostics for support).
func logPath() string {
	dir := dataDir()
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "parley.log")
}
