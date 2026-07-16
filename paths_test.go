package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCompleteNemotronRoot(t *testing.T, root string) {
	t.Helper()
	for _, rel := range []string{
		".ready",
		filepath.Join("runtime", "Scripts", "python.exe"),
		filepath.Join("model", "config.json"),
		filepath.Join("model", "model.safetensors"),
		"server.py",
	} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", path, err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
}

func TestResolveNemotronRootUsesConfiguredInstall(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nemotron")
	writeCompleteNemotronRoot(t, root)
	t.Setenv("PARLEY_NEMOTRON_HOME", root)

	got, err := resolveNemotronRoot()
	if err != nil {
		t.Fatalf("resolveNemotronRoot: %v", err)
	}
	if got != root {
		t.Fatalf("resolveNemotronRoot = %q, want %q", got, root)
	}
}

func TestResolveNemotronRootFollowsSharedSourceMarker(t *testing.T) {
	source := filepath.Join(t.TempDir(), "checkout", "resources", "nemotron")
	writeCompleteNemotronRoot(t, source)
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	sharedServer := filepath.Join(shared, "server.py")
	if err := os.WriteFile(sharedServer, []byte("current provisioner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, ".source-root"), append([]byte{0xef, 0xbb, 0xbf}, []byte(source+"\r\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PARLEY_NEMOTRON_HOME", shared)

	got, err := resolveNemotronRoot()
	if err != nil {
		t.Fatalf("resolveNemotronRoot: %v", err)
	}
	if got != source {
		t.Fatalf("resolveNemotronRoot = %q, want redirected %q", got, source)
	}
	install, err := resolveNemotronInstall()
	if err != nil {
		t.Fatalf("resolveNemotronInstall: %v", err)
	}
	if install.script != sharedServer {
		t.Fatalf("server script = %q, want current shared provisioner %q", install.script, sharedServer)
	}
}
