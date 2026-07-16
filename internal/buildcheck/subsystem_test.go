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

// Existing Parley users may predate optional Nemotron provisioning. An upgrade
// must preserve a complete install, but offer an interactive download when the
// readiness marker is absent instead of silently leaving GPU users on Whisper.
func TestInstallerOffersMissingNemotronOnUpgrade(t *testing.T) {
	path := filepath.Join("..", "..", "build", "windows", "nsis", "project.nsi")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	installer := string(data)
	for _, required := range []string{
		`taskkill /T /F /IM "${PRODUCT_EXECUTABLE}"`,
		`IfFileExists "$INSTDIR\resources\nemotron\.ready"`,
		`StrCpy $NemotronRoot "$LOCALAPPDATA\Parley\nemotron"`,
		`IfFileExists "$NemotronRoot\.source-root"`,
		`Var NemotronProvisionRequested`,
		`Exec '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe"`,
		`resources\nemotron\provision.ps1`,
		`StrCmp $IsUpgrade "0" nemotron_provision`,
		`MessageBox MB_YESNO|MB_ICONQUESTION`,
		`IfSilent nemotron_silent_skip`,
		`${DisableX64FSRedirection}`,
		`nsExec::ExecToStack '"$SYSDIR\nvidia-smi.exe" -L'`,
	} {
		if !strings.Contains(installer, required) {
			t.Fatalf("%s no longer contains %q; missing Nemotron upgrades will not be handled safely", path, required)
		}
	}
	if strings.Contains(installer, "cmd /C nvidia-smi") {
		t.Fatalf("%s probes nvidia-smi through 32-bit cmd; WOW64 redirection hides the System32 executable", path)
	}
	if strings.Contains(installer, "MUI_FINISHPAGE_NOAUTOCLOSE") {
		t.Fatalf("%s forces users to remain on the installer details page after setup completes", path)
	}
	if strings.Contains(installer, `nsExec::ExecToLog '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\resources\nemotron\setup.ps1"`) {
		t.Fatalf("%s blocks the installer UI on Nemotron provisioning", path)
	}
	if strings.Contains(installer, `File /r "..\..\..\resources"`) {
		t.Fatalf("%s recursively bundles all generated Nemotron assets from a developer checkout", path)
	}
}

// Upgrade bookkeeping must not recursively scan the full installation tree.
// Legacy releases stored the optional multi-GB Nemotron runtime under INSTDIR;
// Wails' generated writeUninstaller macro therefore made otherwise-complete
// upgrades appear to grow indefinitely while its GetSize call enumerated it.
func TestInstallerSizeEstimateExcludesPersistentNemotron(t *testing.T) {
	path := filepath.Join("..", "..", "build", "windows", "nsis", "project.nsi")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	installer := string(data)
	for _, required := range []string{
		`!macro parley.writeUninstaller`,
		`${GetSize} "$INSTDIR\resources\whisper"`,
		`!insertmacro parley.writeUninstaller`,
	} {
		if !strings.Contains(installer, required) {
			t.Fatalf("%s no longer contains %q; upgrade size bookkeeping may include persistent Nemotron data", path, required)
		}
	}
	if strings.Contains(installer, `!insertmacro wails.writeUninstaller`) {
		t.Fatalf("%s calls Wails' generated uninstaller macro, which recursively scans all of INSTDIR", path)
	}
	if strings.Contains(installer, `${GetSize} "$INSTDIR"`) {
		t.Fatalf("%s recursively scans all of INSTDIR while registering EstimatedSize", path)
	}
}

func TestNemotronSetupUsesPrivatePythonAndSharedStorage(t *testing.T) {
	path := filepath.Join("..", "..", "resources", "nemotron", "setup.ps1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	setup := string(data)
	for _, required := range []string{
		`Join-Path $env:LOCALAPPDATA "Parley/nemotron"`,
		`Invoke-Checked $uvExe python install 3.11 --no-bin`,
		`Test-CompleteNemotronInstall`,
		`Write-SourceRoot`,
		`$env:HF_TOKEN_PATH = $existingToken`,
		`$env:HF_XET_HIGH_PERFORMANCE = "1"`,
	} {
		if !strings.Contains(setup, required) {
			t.Fatalf("%s no longer contains %q", path, required)
		}
	}
}
