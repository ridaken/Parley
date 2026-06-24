# Parley — implementation plan & phase status

Status legend: ✅ done · 🚧 in progress · ⏳ planned

## Phase 0 — De-risk spikes ✅
- ✅ `malgo` dual capture (mic + WASAPI loopback) → 16k mono → WAV (`internal/audio`).
- ✅ Bundled `whisper.cpp` server spawned from Go, chunked HTTP transcription (`internal/stt`).
- ✅ Wails 3 skeleton: React + shadcn, Go↔JS events (`status` / `transcript` / `analysis`).

## Phase 1 — MVP vertical slice ✅
- ✅ capture → chunked STT → live You/Others transcript → per-source audio on disk.

## Phase 2 — Analysis loop ✅
- ✅ Context editor + reusable profiles (`ContextDialog`, `profiles` table).
- ✅ Cadence-driven structured JSON analysis with topic-diff archiving (`internal/analysis`).
- ✅ Current Topic / Assertions / Past Topics / Suggestions panels.
- ✅ LLM endpoint/model/key settings; API key in OS keychain.

## Phase 2.5 — Live context injection ✅ *(new)*
User can inject context into the running analysis via an input box.
- ✅ Scoped notes: **meeting** (standing facts, persist all session) vs **topic**
  (correct the current topic, auto-expire when the topic changes) — solves the
  "X not Y" staleness problem. Engine drops topic notes on topic change
  (`Engine.AddLiveNote` / `dropTopicNotesLocked` / prompt sections).
- ✅ Frontend `LiveContextBar` with scope toggle + active-note chips.
- ✅ Notes persisted with the session and restored on resume.

## Phase 2.7 — Session persistence + save / load / resume ✅ *(new)*
Save and load full meeting state so multi-part meetings persist.
- ✅ SQLite schema: `sessions`, `transcript_segments`, `live_notes`; analysis snapshot
  stored as JSON on the session row (`internal/store/sessions.go`).
- ✅ **Auto-save continuously** — segments + analysis written as they happen
  (crash-safe), session end-stamped on Stop.
- ✅ `MeetingService`: `ListSessions` / `LoadSession` / `Resume` / `RenameSession` /
  `DeleteSession`; `Resume` rehydrates the engine (`Engine.Restore`) and appends.
- ✅ Frontend `SessionsDialog` (view / resume / rename / delete) + "viewing saved
  meeting" banner and Resume flow in `App.tsx`.

## Phase 2.8 — Robustness fixes ✅ *(new, from field testing)*
- ✅ **Configurable / smaller bundled model** + **remote STT URL** option
  (`Settings.WhisperModel`, `Settings.SttBaseURL`); local engine skipped when remote set.
- ✅ **Mic indicator accuracy** — status now reports mic presence from the sources that
  actually started (`Capturer.HasMic` / `ActiveSpecs`), not a label guess.
- ✅ **Error visibility** — friendly error string surfaced in the GUI (red banner) and
  full diagnostics written to `parley.log`; settings-dialog errors wrap + are explained.
- ✅ **Chunker tail-flush bug** — the final partial chunk on Stop was transcribed with an
  already-cancelled context (silently dropped, up to ~5s of audio lost). Fixed: the final
  flush runs on a fresh context and waits for its transcription before tearing down.

## Phase 2.9 — Pre-meeting context polish ✅ *(new)*
Make the existing context-profile system the obvious place to prep the LLM before
a meeting, and tame large pasted documents.
- ✅ **Idle setup strip** in `App.tsx`: when idle (not recording, not viewing a
  saved meeting) it shows the active context profile — or a "no context set"
  prompt — with a button into the context editor, so background is set *before*
  Start instead of only via the unlabeled header icon.
- ✅ **Condense with AI** in `ContextDialog`: an opt-in button shrinks the free-form
  notes (where pasted docs land) via the **active LLM connection**
  (`LibraryService.CondenseContext`), preserving names/acronyms/dates/decisions and
  stripping redundancy. Result is shown as a **before/after preview** the user
  accepts or discards — the saved notes are never silently rewritten.

## Test coverage
Full suite passes with cgo enabled (`go test ./internal/... .`) and clean under `-race`:
- `store`: settings, profile CRUD, **session save/load/delete round-trip**.
- `analysis`: JSON parse, emit, **topic-diff archiving**, **live-note scoping/expiry**,
  **resume/Restore seeding**.
- `stt`: **chunker windowing + timeline + silence-skip**, peak amplitude, server headless.
- `llm`: completion success, HTTP/API error surfacing, base-URL normalisation.
- `audio`: device listing, **WAV encode + incremental writer header patching**.
Verified end-to-end: `go build`/`go vet` clean, `wails3 generate bindings` clean, frontend
`tsc` + production `vite build` clean.

## Phase 3 — Polish ⏳
- ⏳ Session history **full-text search**; export (Markdown minutes / action items).
- ⏳ Whisper **model picker** with on-demand download; GPU build when NVIDIA present.
- ⏳ Global hotkeys + "mark moment" bookmarks; recording-consent indicator.
- ⏳ Talk-time analytics; dark/light theme; left-edge scrollbar + empty-state polish
  (see `POLISH-BACKLOG.md`).

## Phase 4 — Cross-platform + LLM bundling ⏳
- ⏳ **Cross-platform bundled-engine launcher** — the whisper path is currently
  Windows-only (`bin/Release/whisper-server.exe`); macOS/Linux use the remote-URL option
  for now. Add per-OS binary resolution.
- ⏳ macOS system audio (ScreenCaptureKit / virtual device); Linux PipeWire/Pulse monitor.
- ⏳ Optional bundled/auto-launched llama-server + model download.

## Known gaps / notes
- Resumed parts write audio to separate `recordings/session-<id>/part-*` folders; parts
  are not yet merged into one file.
- `DeleteSession` is blocked for the meeting currently recording.
