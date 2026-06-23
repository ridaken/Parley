// Command audiospike is a Phase-0 de-risk spike: capture the microphone AND the
// system output (WASAPI loopback) simultaneously, both resampled to 16 kHz mono,
// and write a stereo WAV (left = mic / "You", right = system / "Others").
//
// Run with audio playing on your default output device, and speak into your mic:
//
//	go run ./cmd/audiospike
//
// It prints per-channel level stats so you can tell whether real signal was
// captured on each side, then writes capture.wav.
package main

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
	"github.com/tomvokac/parley/internal/audio"
)

const (
	sampleRate = 16000
	duration   = 5 * time.Second
	outPath    = "capture.wav"

	// Self-test tone played through the default output so loopback capture can
	// be validated even with no external audio source.
	playRate     = 48000
	playChannels = 2
	toneHz       = 440.0
)

// sink accumulates S16 mono samples delivered by a malgo capture callback.
type sink struct {
	mu      sync.Mutex
	samples []int16
}

func (s *sink) onData(_, in []byte, frames uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// in is little-endian S16 mono: 2 bytes per frame.
	for i := 0; i+1 < len(in); i += 2 {
		s.samples = append(s.samples, int16(in[i])|int16(in[i+1])<<8)
	}
	_ = frames
}

func (s *sink) snapshot() []int16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int16, len(s.samples))
	copy(out, s.samples)
	return out
}

func levels(name string, s []int16) {
	if len(s) == 0 {
		fmt.Printf("  %-8s no samples captured\n", name)
		return
	}
	var peak int
	var sumSq float64
	for _, v := range s {
		a := int(v)
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
		sumSq += float64(v) * float64(v)
	}
	rms := math.Sqrt(sumSq / float64(len(s)))
	dur := float64(len(s)) / float64(sampleRate)
	fmt.Printf("  %-8s %d samples (%.2fs)  peak=%d (%.1f%%)  rms=%.0f\n",
		name, len(s), dur, peak, float64(peak)/327.67, rms)
}

// startTone starts a playback device emitting a sine wave on the default output.
func startTone(ctx malgo.Context) (*malgo.Device, error) {
	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = malgo.FormatS16
	cfg.Playback.Channels = playChannels
	cfg.SampleRate = playRate

	var phase atomic.Uint64 // sample index, advanced in the audio thread
	onData := func(out, _ []byte, frames uint32) {
		idx := phase.Load()
		for f := uint32(0); f < frames; f++ {
			t := float64(idx+uint64(f)) / float64(playRate)
			v := int16(math.Sin(2*math.Pi*toneHz*t) * 0.3 * 32767)
			for c := 0; c < playChannels; c++ {
				b := (int(f)*playChannels + c) * 2
				if b+1 < len(out) {
					out[b] = byte(v)
					out[b+1] = byte(v >> 8)
				}
			}
		}
		phase.Add(uint64(frames))
	}

	dev, err := malgo.InitDevice(ctx, cfg, malgo.DeviceCallbacks{Data: onData})
	if err != nil {
		return nil, err
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return nil, err
	}
	return dev, nil
}

func startCapture(ctx malgo.Context, devType malgo.DeviceType, s *sink) (*malgo.Device, error) {
	cfg := malgo.DefaultDeviceConfig(devType)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = 1
	cfg.SampleRate = sampleRate
	cfg.Alsa.NoMMap = 1

	dev, err := malgo.InitDevice(ctx, cfg, malgo.DeviceCallbacks{Data: s.onData})
	if err != nil {
		return nil, err
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return nil, err
	}
	return dev, nil
}

func main() {
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		panic(err)
	}
	defer func() { _ = mctx.Uninit(); mctx.Free() }()

	// Show what we're capturing from.
	if infos, err := mctx.Devices(malgo.Capture); err == nil {
		fmt.Println("Capture (input) devices:")
		for _, d := range infos {
			fmt.Printf("  - %s\n", d.Name())
		}
	}
	if infos, err := mctx.Devices(malgo.Playback); err == nil {
		fmt.Println("Playback (output) devices [loopback source]:")
		for _, d := range infos {
			fmt.Printf("  - %s\n", d.Name())
		}
	}

	var mic, sys sink

	// Mic is optional — this machine may have no input device.
	micDev, err := startCapture(mctx.Context, malgo.Capture, &mic)
	if err != nil {
		fmt.Printf("\n[warn] mic capture unavailable (%v) — continuing with loopback only\n", err)
	} else {
		defer func() { micDev.Uninit() }()
	}

	// Play a self-test tone so loopback capture has a signal to pick up.
	toneDev, err := startTone(mctx.Context)
	if err != nil {
		fmt.Printf("[warn] tone playback failed (%v)\n", err)
	} else {
		defer func() { toneDev.Uninit() }()
	}

	sysDev, err := startCapture(mctx.Context, malgo.Loopback, &sys)
	if err != nil {
		panic(fmt.Errorf("loopback capture: %w", err))
	}
	defer func() { sysDev.Uninit() }()

	fmt.Printf("\nCapturing for %s (playing a %.0fHz self-test tone on the default output)...\n", duration, toneHz)
	time.Sleep(duration)

	if micDev != nil {
		micDev.Stop()
	}
	if toneDev != nil {
		toneDev.Stop()
	}
	sysDev.Stop()

	left := mic.snapshot()
	right := sys.snapshot()

	fmt.Println("\nLevels:")
	levels("mic", left)
	levels("system", right)

	if err := audio.WriteStereoWAV(outPath, sampleRate, left, right); err != nil {
		panic(err)
	}
	fmt.Printf("\nWrote %s (L=You/mic, R=Others/system)\n", outPath)
}
