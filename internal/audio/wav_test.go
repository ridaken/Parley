package audio

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestEncodeMonoWAVHeader(t *testing.T) {
	samples := []int16{1, -2, 3, -4, 5}
	wav := EncodeMonoWAV(SampleRate, samples)

	want := 44 + len(samples)*2
	if len(wav) != want {
		t.Fatalf("len = %d, want %d", len(wav), want)
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[12:16]) != "fmt " {
		t.Fatalf("bad header magic: %q", wav[0:16])
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != uint32(len(samples)*2) {
		t.Fatalf("data size = %d, want %d", got, len(samples)*2)
	}
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != uint32(SampleRate) {
		t.Fatalf("sample rate = %d, want %d", got, SampleRate)
	}
}

func TestMonoWAVWriterPatchesSizesOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec.wav")
	w, err := NewMonoWAVWriter(path, SampleRate)
	if err != nil {
		t.Fatalf("NewMonoWAVWriter: %v", err)
	}
	if err := w.Write([]int16{10, 20, 30}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Write([]int16{40, 50}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	const dataBytes = 5 * 2
	if len(data) != 44+dataBytes {
		t.Fatalf("file size = %d, want %d", len(data), 44+dataBytes)
	}
	if got := binary.LittleEndian.Uint32(data[4:8]); got != uint32(36+dataBytes) {
		t.Fatalf("RIFF chunk size = %d, want %d", got, 36+dataBytes)
	}
	if got := binary.LittleEndian.Uint32(data[40:44]); got != uint32(dataBytes) {
		t.Fatalf("data chunk size = %d, want %d", got, dataBytes)
	}
}
