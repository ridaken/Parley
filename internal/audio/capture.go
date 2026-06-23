package audio

import (
	"fmt"

	"github.com/gen2brain/malgo"
)

// SampleRate is the mono PCM rate Parley captures and feeds to whisper.
const SampleRate = 16000

// Source is the transcript label applied to audio from a given capture source.
type Source string

const (
	// You is the local user's own microphone (attributable).
	You Source = "You"
	// Others is remote/other participants (e.g. system loopback).
	Others Source = "Others"
	// Room is an in-person/mixed capture where individual speakers — including
	// the user — cannot be separated.
	Room Source = "Room"
)

// Device kinds.
const (
	KindInput    = "input"    // a capture device (microphone / line-in)
	KindLoopback = "loopback" // a playback device captured via loopback
)

// DeviceInfo describes an enumerated audio device.
type DeviceInfo struct {
	ID        string `json:"id"`   // hex token (stable per device); "" = system default
	Name      string `json:"name"`
	Kind      string `json:"kind"` // "input" (capture) | "output" (playback)
	IsDefault bool   `json:"isDefault"`
}

// SourceSpec selects a device to capture and the label to apply to its audio.
type SourceSpec struct {
	ID    string // device hex token; "" = default device for the Kind
	Kind  string // KindInput | KindLoopback
	Label Source // transcript label (You/Others/Room or custom)
}

// ListDevices enumerates input (capture) and output (playback) devices.
func ListDevices() ([]DeviceInfo, error) {
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return nil, err
	}
	defer func() { _ = mctx.Uninit(); mctx.Free() }()

	var devices []DeviceInfo
	if infos, e := mctx.Devices(malgo.Capture); e == nil {
		for i := range infos {
			devices = append(devices, DeviceInfo{
				ID:        infos[i].ID.String(),
				Name:      infos[i].Name(),
				Kind:      "input",
				IsDefault: infos[i].IsDefault != 0,
			})
		}
	}
	if infos, e := mctx.Devices(malgo.Playback); e == nil {
		for i := range infos {
			devices = append(devices, DeviceInfo{
				ID:        infos[i].ID.String(),
				Name:      infos[i].Name(),
				Kind:      "output",
				IsDefault: infos[i].IsDefault != 0,
			})
		}
	}
	return devices, nil
}

// Capturer captures one or more selected sources concurrently, each as 16 kHz
// mono S16, delivering copied sample slices (tagged with the source label) to
// onSamples.
type Capturer struct {
	mctx      *malgo.AllocatedContext
	devices   []*malgo.Device
	onSamples func(Source, []int16)
	active    []Source
}

// NewCapturer initialises an audio context.
func NewCapturer(onSamples func(Source, []int16)) (*Capturer, error) {
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return nil, fmt.Errorf("init audio context: %w", err)
	}
	return &Capturer{mctx: mctx, onSamples: onSamples}, nil
}

// Start opens every requested source (best-effort: a source that fails to open
// is logged and skipped). It errors only if no source could be started.
func (c *Capturer) Start(specs []SourceSpec) error {
	captureInfos, _ := c.mctx.Devices(malgo.Capture)
	playbackInfos, _ := c.mctx.Devices(malgo.Playback)

	for _, sp := range specs {
		dev, err := c.openSource(sp, captureInfos, playbackInfos)
		if err != nil {
			fmt.Printf("[audio] source %q (%s) unavailable: %v\n", sp.Label, sp.Kind, err)
			continue
		}
		c.devices = append(c.devices, dev)
		c.active = append(c.active, sp.Label)
	}
	if len(c.devices) == 0 {
		c.Stop()
		return fmt.Errorf("no audio sources could be started")
	}
	return nil
}

func (c *Capturer) openSource(sp SourceSpec, captureInfos, playbackInfos []malgo.DeviceInfo) (*malgo.Device, error) {
	var devType malgo.DeviceType
	var pool []malgo.DeviceInfo
	switch sp.Kind {
	case KindInput:
		devType, pool = malgo.Capture, captureInfos
	case KindLoopback:
		// Loopback captures a playback device's output; match against playbacks.
		devType, pool = malgo.Loopback, playbackInfos
	default:
		return nil, fmt.Errorf("unknown source kind %q", sp.Kind)
	}

	cfg := malgo.DefaultDeviceConfig(devType)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = 1
	cfg.SampleRate = SampleRate
	cfg.Alsa.NoMMap = 1

	if sp.ID != "" {
		for i := range pool {
			if pool[i].ID.String() == sp.ID {
				cfg.Capture.DeviceID = pool[i].ID.Pointer()
				break
			}
		}
		// Not found → fall back to the default device (nil ID).
	}

	label := sp.Label
	onData := func(_, in []byte, _ uint32) {
		n := len(in) / 2
		if n == 0 {
			return
		}
		samples := make([]int16, n)
		for i := 0; i < n; i++ {
			samples[i] = int16(in[2*i]) | int16(in[2*i+1])<<8
		}
		c.onSamples(label, samples)
	}

	dev, err := malgo.InitDevice(c.mctx.Context, cfg, malgo.DeviceCallbacks{Data: onData})
	if err != nil {
		return nil, err
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return nil, err
	}
	return dev, nil
}

// Active returns the labels of the sources that started successfully.
func (c *Capturer) Active() []Source { return c.active }

// Stop halts all devices and frees the context.
func (c *Capturer) Stop() {
	for _, d := range c.devices {
		d.Stop()
		d.Uninit()
	}
	c.devices = nil
	if c.mctx != nil {
		_ = c.mctx.Uninit()
		c.mctx.Free()
		c.mctx = nil
	}
}
