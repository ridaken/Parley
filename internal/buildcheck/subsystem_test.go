// Package buildcheck holds regression guards over the build configuration.
package buildcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The production Windows build MUST link as a GUI-subsystem binary
// (`-H windowsgui`); otherwise Windows allocates a console window that lingers
// at launch. This guards against that linker flag being dropped from the build
// config again. (At runtime, main.hideOwnedConsole is a second line of defence.)
func TestWindowsProductionBuildIsGUISubsystem(t *testing.T) {
	path := filepath.Join("..", "..", "build", "windows", "Taskfile.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), "-H windowsgui") {
		t.Fatalf("%s no longer passes -H windowsgui for the production build — "+
			"a console window will appear at launch. Restore it in the non-DEV "+
			"BUILD_FLAGS ldflags.", path)
	}
}
