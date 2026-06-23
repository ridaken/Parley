package audio

import "testing"

// TestListDevices is a smoke test against the real audio backend on this machine.
func TestListDevices(t *testing.T) {
	devices, err := ListDevices()
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	for _, d := range devices {
		t.Logf("%-7s default=%-5v id=%s name=%s", d.Kind, d.IsDefault, d.ID, d.Name)
		if d.Kind != "input" && d.Kind != "output" {
			t.Errorf("unexpected kind %q", d.Kind)
		}
	}
	if len(devices) == 0 {
		t.Skip("no audio devices on this machine")
	}
}
