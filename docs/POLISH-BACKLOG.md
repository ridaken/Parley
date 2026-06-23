# Polish & enhancement backlog

Deferred refinements noted during development. These are **not yet implemented** — they're
captured here so they aren't lost while we build out core phases.

## Phase 1 UI polish
- **Left-edge scrollbar line.** A faint full-height vertical line hugs the far-left edge of
  the window (the transcript `ScrollArea` viewport/scrollbar bleeding to the edge). Fix with
  padding/overflow handling on the viewport, or inset the scrollbar.
- **Real device selection.** Today capture uses the OS default input + default output
  (loopback). Add dropdowns to pick a specific microphone and a specific output device to
  loop back, and pass the chosen `DeviceID`s into `malgo.DeviceConfig`.
- **Audio level meters.** Show live VU meters per source (You/Others) so the user can
  confirm audio is actually being captured before relying on the transcript.
- **Recording indicator.** Surface that audio is being saved, where, and the elapsed size;
  a button to open the recordings folder.
- **Empty-state vertical centering.** The panel hint text isn't perfectly centered in tall
  cards; tighten the flex layout.
- **App icon.** Replace the default Wails "W" titlebar/taskbar icon with a Parley icon.

## Transcription quality
- **Partial / streaming transcripts.** Currently chunks are transcribed every 5s (whole
  windows). Consider overlapping windows or `whisper-stream` for lower latency and to avoid
  clipping words at chunk boundaries.
- **VAD-based segmentation.** Use the bundled `whisper-vad-speech-segments` /
  `test-vad` capability (or Silero) to cut on speech boundaries instead of fixed time, which
  improves accuracy and avoids transcribing silence.
- **Custom vocabulary / initial prompt.** Feed names/jargon from the context profile as
  whisper's `prompt` to bias recognition of proper nouns.
- **GPU acceleration.** Offer the CUDA whisper build when an NVIDIA GPU is present for much
  faster transcription.
- **Model picker.** Let the user choose tiny/base/small/medium and download on demand.

## Audio robustness
- **Echo / double-capture handling.** When the user is on speakers (not headphones), their
  own voice is captured by both mic and loopback. Add optional echo cancellation/dedupe.
- **Device hot-swap.** Detect device changes mid-session and re-open capture gracefully.
- **Mixed recording.** In addition to per-source `you.wav`/`others.wav`, optionally write a
  single mixed/stereo session file.

## Cross-platform (Phase 4 territory, noted here)
- macOS system audio via ScreenCaptureKit (or a virtual device); Linux via PipeWire/Pulse
  monitor sources.

## Misc
- Global hotkeys (start/stop, "mark moment" bookmarks).
- Recording-consent indicator + jurisdiction note.
- Talk-time analytics (You vs Others speaking ratio).
- Export session as Markdown minutes / action items.
