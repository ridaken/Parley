// Package audio contains capture and encoding helpers for Parley.
package audio

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
)

const bitsPerSample = 16

// writeWAVHeader writes a 44-byte canonical PCM WAV header. dataBytes is the
// size of the PCM payload that will follow.
func writeWAVHeader(w io.Writer, sampleRate, channels, dataBytes int) error {
	byteRate := sampleRate * channels * (bitsPerSample / 8)
	blockAlign := channels * (bitsPerSample / 8)

	if _, err := io.WriteString(w, "RIFF"); err != nil {
		return err
	}
	fields := []any{
		uint32(36 + dataBytes), // ChunkSize
	}
	if err := writeLE(w, fields...); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "WAVE"); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "fmt "); err != nil {
		return err
	}
	if err := writeLE(w,
		uint32(16),            // Subchunk1Size (PCM)
		uint16(1),             // AudioFormat = PCM
		uint16(channels),      // NumChannels
		uint32(sampleRate),    // SampleRate
		uint32(byteRate),      // ByteRate
		uint16(blockAlign),    // BlockAlign
		uint16(bitsPerSample), // BitsPerSample
	); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "data"); err != nil {
		return err
	}
	return writeLE(w, uint32(dataBytes))
}

func writeLE(w io.Writer, vals ...any) error {
	for _, v := range vals {
		if err := binary.Write(w, binary.LittleEndian, v); err != nil {
			return err
		}
	}
	return nil
}

// EncodeMonoWAV returns a complete WAV file (header + PCM) for the given 16-bit
// mono samples. Used to POST audio chunks to the whisper server.
func EncodeMonoWAV(sampleRate int, samples []int16) []byte {
	var buf bytes.Buffer
	_ = writeWAVHeader(&buf, sampleRate, 1, len(samples)*2)
	_ = binary.Write(&buf, binary.LittleEndian, samples)
	return buf.Bytes()
}

// WriteStereoWAV writes interleaved 16-bit PCM stereo samples to a WAV file at
// the given sample rate. left and right are mono S16 sample slices; the shorter
// is zero-padded so both channels stay time-aligned.
func WriteStereoWAV(path string, sampleRate int, left, right []int16) error {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := writeWAVHeader(f, sampleRate, 2, n*2*2); err != nil {
		return err
	}
	frame := make([]int16, 2)
	for i := 0; i < n; i++ {
		if i < len(left) {
			frame[0] = left[i]
		} else {
			frame[0] = 0
		}
		if i < len(right) {
			frame[1] = right[i]
		} else {
			frame[1] = 0
		}
		if err := binary.Write(f, binary.LittleEndian, frame); err != nil {
			return err
		}
	}
	return nil
}

// MonoWAVWriter streams 16-bit mono samples to a WAV file, patching the header
// sizes on Close. Safe for incremental recording during a session.
type MonoWAVWriter struct {
	f          *os.File
	sampleRate int
	dataBytes  int
}

// NewMonoWAVWriter creates a WAV file and writes a placeholder header.
func NewMonoWAVWriter(path string, sampleRate int) (*MonoWAVWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if err := writeWAVHeader(f, sampleRate, 1, 0); err != nil {
		f.Close()
		return nil, err
	}
	return &MonoWAVWriter{f: f, sampleRate: sampleRate}, nil
}

// Write appends mono samples.
func (w *MonoWAVWriter) Write(samples []int16) error {
	if len(samples) == 0 {
		return nil
	}
	if err := binary.Write(w.f, binary.LittleEndian, samples); err != nil {
		return err
	}
	w.dataBytes += len(samples) * 2
	return nil
}

// Close patches the RIFF/data sizes and closes the file.
func (w *MonoWAVWriter) Close() error {
	if w.f == nil {
		return nil
	}
	defer func() { w.f = nil }()
	// ChunkSize @ offset 4
	if _, err := w.f.Seek(4, io.SeekStart); err == nil {
		_ = binary.Write(w.f, binary.LittleEndian, uint32(36+w.dataBytes))
	}
	// Subchunk2Size @ offset 40
	if _, err := w.f.Seek(40, io.SeekStart); err == nil {
		_ = binary.Write(w.f, binary.LittleEndian, uint32(w.dataBytes))
	}
	return w.f.Close()
}
