package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveResource locates a local transcription resource by checking
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

type nemotronInstall struct {
	root   string
	script string
}

// resolveNemotronInstall finds a complete Nemotron installation. New installs use
// a stable per-user location so development and packaged builds share the same
// multi-GB model. The resource lookup remains as a compatibility path for older
// checkouts/installers that provisioned next to the executable.
func resolveNemotronInstall() (nemotronInstall, error) {
	var roots []string
	if configured := strings.TrimSpace(os.Getenv("PARLEY_NEMOTRON_HOME")); configured != "" {
		roots = append(roots, configured)
	}
	if localAppData, err := os.UserCacheDir(); err == nil {
		roots = append(roots, filepath.Join(localAppData, "Parley", "nemotron"))
	}
	if ready, err := resolveResource(filepath.Join("resources", "nemotron", ".ready")); err == nil {
		roots = append(roots, filepath.Dir(ready))
	}

	seen := make(map[string]bool)
	var searched []string
	for _, candidate := range roots {
		root, err := filepath.Abs(candidate)
		if err != nil || seen[root] {
			continue
		}
		seen[root] = true
		searched = append(searched, root)

		provisionerRoot := root
		if source, err := os.ReadFile(filepath.Join(root, ".source-root")); err == nil {
			redirected := strings.TrimSpace(strings.TrimPrefix(string(source), "\ufeff"))
			if redirected != "" {
				root = redirected
				searched = append(searched, root)
			}
		}
		if completeNemotronRoot(root) {
			script := filepath.Join(provisionerRoot, "server.py")
			if _, err := os.Stat(script); err != nil {
				script = filepath.Join(root, "server.py")
			}
			if _, err := os.Stat(script); err == nil {
				return nemotronInstall{root: root, script: script}, nil
			}
		}
	}
	return nemotronInstall{}, fmt.Errorf("could not locate a complete Nemotron installation (searched %v)", searched)
}

func resolveNemotronRoot() (string, error) {
	install, err := resolveNemotronInstall()
	return install.root, err
}

func completeNemotronRoot(root string) bool {
	for _, rel := range []string{
		".ready",
		filepath.Join("runtime", "Scripts", "python.exe"),
		filepath.Join("model", "config.json"),
		filepath.Join("model", "model.safetensors"),
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			return false
		}
	}
	return true
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
