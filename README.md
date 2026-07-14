# Parley — local-first meeting assistant

Parley listens to a live conversation (your microphone **and** the computer's own
output audio, mixed), transcribes it locally, and uses an LLM to surface — in real
time — the **current topic**, **assertions** made (attributed to *You* vs *Others*),
a history of **past topics**, and **suggested questions**.

It is **local-first**: audio and transcription stay on your machine. The only
optional outbound path is an LLM endpoint you configure (which can also be local).

- **Stack:** Wails 3 (alpha) · Go backend · React + TypeScript + Tailwind/shadcn UI
- **Transcription:** bundled `whisper.cpp` server (or a remote one you point it at)
- **LLM:** any OpenAI-compatible endpoint (local llama-server / LM Studio / Ollama, or cloud)

---

## What works today

- 🎙️ **Dual capture** — your mic (*You*) + system/loopback audio (*Others*), mixed.
- 📝 **Live transcript** with speaker labels, recorded to disk per source.
- 🧠 **Live analysis** — current topic, assertions, past topics, suggested questions.
- 🗒️ **Reusable context profiles** — agenda, attendees, notes to ground the analysis.
- 💬 **Live context injection** — correct or add context mid-meeting (see below).
- 💾 **Save / load / resume meetings** — every meeting is auto-saved; reopen it to
  review, or **resume** it to keep recording (multi-part meetings).
- 🔌 **Bring-your-own transcription** — offload STT to a remote whisper.cpp server.
- 🧰 **Saved LLM connections** — store multiple providers (local + cloud) and switch
  between them per meeting from the header, without re-entering URLs/keys each time.

---

## How it works

Parley is a pipeline: audio is captured per source, sliced into fixed windows,
transcribed locally, and the rolling transcript is periodically summarised by an
LLM into the topic/assertions/suggestions you see on screen.

```mermaid
flowchart TD
    subgraph Capture["Audio capture (cgo / miniaudio)"]
        MIC["🎙️ Mic — labelled You"]
        SYS["🔊 System loopback — labelled Others"]
    end

    MIC -->|"16 kHz mono int16"| CH["Chunker"]
    SYS -->|"16 kHz mono int16"| CH

    CH -->|"every 5 s, per source<br/>(silent windows skipped)"| WAV["Encode mono WAV"]
    WAV -->|"POST multipart /inference"| WS["whisper.cpp server<br/>(bundled subprocess or remote URL)"]
    WS -->|"JSON: text field"| SEG["Segment: source, text, startMs, endMs"]

    SEG --> UI1["Live transcript panel"]
    SEG --> DB[("SQLite<br/>auto-save")]
    SEG --> ENG["Analysis engine<br/>(rolling transcript buffer)"]

    NOTES["🗒️ Live context notes<br/>(This topic / Whole meeting)"] --> ENG
    PROFILE["📋 Meeting context profile<br/>(summary / people / notes)"] --> ENG

    ENG -->|"every analysis interval<br/>(default 15 s, min 3 s)"| PROMPT["Build prompt:<br/>context + notes + last 60 lines"]
    PROMPT -->|"POST /chat/completions<br/>(OpenAI-compatible)"| LLM["LLM endpoint<br/>(local or cloud)"]
    LLM -->|"minified JSON reply"| STATE["State: current topic,<br/>assertions, past topics, suggestions"]
    STATE --> UI2["Analysis panels"]
    STATE --> DB
```

Only the **LLM** call leaves the machine (and only if you point it at a remote
endpoint). Audio and transcription stay local.

### The analysis interval

A ticker fires every **`analysisIntervalSec`** seconds (Settings → *Analysis
interval*, default **15 s**, floor **3 s**). On each tick the engine
([`internal/analysis/engine.go`](internal/analysis/engine.go)):

1. **Skips** the tick if the previous analysis is still in flight, or if no new
   transcript lines have arrived since the last run (no churn on a quiet meeting).
2. Builds a prompt from the **last 60 transcript lines** (`promptWindowLines`) plus
   the meeting context and any in-effect live notes.
3. Sends one **non-streaming** chat completion (`temperature: 0.2`) and parses the
   reply. The whole transcript buffer is capped at 600 lines, and at most 30 past
   topics are retained.

A shorter interval = fresher insight but more LLM calls; a longer interval is
cheaper and calmer. Transcription is independent of this — it always runs on its
own 5-second cadence.

### How topics are decided

The LLM is asked to return a `topicChanged` boolean alongside the current topic
title. The engine only **rolls over** a topic when *all* of these hold: the model
says `topicChanged: true`, there is an existing current topic, and the new title
actually differs (case-insensitive) from the previous one. On rollover the previous
topic is archived into **Past topics**, and any **topic-scoped** live notes are
dropped so a stale correction can't bleed into the next topic.

### HTTP payloads

**Transcription** — `POST {sttURL}/inference`, `multipart/form-data`:

| field | value |
|-------|-------|
| `file` | `chunk.wav` (mono, 16 kHz, signed-16 PCM WAV) |
| `response_format` | `json` |
| `temperature` | `0.0` |

Response (whisper.cpp): `{ "text": "the transcribed text" }`

**Analysis** — `POST {llmBaseURL}/chat/completions` (OpenAI-compatible),
`Authorization: Bearer <key>` if a key is set:

```jsonc
{
  "model": "local-model",
  "messages": [
    { "role": "system", "content": "You monitor a live meeting transcript… return ONLY a JSON object {currentTopicTitle, currentTopicSummary, topicChanged, assertions[], suggestions[]}" },
    { "role": "user",   "content": "MEETING CONTEXT … RECENT TRANSCRIPT …" }
  ],
  "temperature": 0.2,
  "stream": false
}
```

The reply's `choices[0].message.content` is a minified JSON object that becomes the
analysis State:

```json
{"currentTopicTitle":"Q3 pricing","currentTopicSummary":"Debating list price vs. discount floor.","topicChanged":true,"assertions":[{"speaker":"Others","text":"Margin can't drop below 40%."}],"suggestions":[{"kind":"question","text":"What's the volume threshold for the discount?"}]}
```

### How your context reaches the LLM

Two kinds of user-supplied context are folded into the **user** message every
analysis tick (see `buildUserPrompt`):

- **Meeting context profile** (notebook icon) — a reusable agenda/attendees/notes
  block, emitted as a `MEETING CONTEXT` header.
- **Live notes** typed during the meeting, by scope:
  - **Whole meeting** → listed under **STANDING CORRECTIONS** (names, acronyms,
    themes); they ride along on *every* subsequent tick for the whole session.
  - **This topic** → listed under **NOTE ON CURRENT TOPIC** and trusted over the
    transcript; they expire automatically when the topic rolls over.

The assembled user prompt looks like:

```text
MEETING CONTEXT
Summary: Weekly account sync with Acme
People: Dana (us), Priya (Acme)
Notes: Renewal due end of quarter

STANDING CORRECTIONS (apply to the whole meeting — e.g. correct names, acronyms, themes):
- The client is Acme — A-C-M-E

NOTE ON CURRENT TOPIC (corrects the immediate discussion only — trust over the transcript if they conflict):
- This is about gross margin, not revenue

PREVIOUS TOPIC TITLE: Renewal timeline

RECENT TRANSCRIPT. Speaker labels: "You" = the listener; "Others" = remote participants; "Room" = mixed in-person capture:
You: so on margin, where do we land?
Others: we can't go below forty percent…

Return the JSON object now.
```

> 🔧 **Keep this section current:** if you change the chunk window, the analysis
> cadence, the prompt shape, or either HTTP contract, update the diagram and the
> payloads above so the README stays the source of truth for how Parley works.

---

## Prerequisites

| Tool | Notes |
|------|-------|
| [Go](https://go.dev/dl/) 1.25+ | Backend. |
| Node.js 18+ / npm | Frontend. |
| [Wails 3 CLI](https://v3.wails.io/) | `go install github.com/wailsapp/wails/v3/cmd/wails3@latest` |
| [Task](https://taskfile.dev/) | **Optional** shortcut runner for `Taskfile.yml`. See the note below. |
| **A C compiler (cgo)** | Required by the audio library (`malgo`). See per-OS notes. |

> **Seeing `'task' is not recognized`?** `task` is the optional [Task](https://taskfile.dev/)
> runner — a separate tool, **not** a Windows built-in — so that error just means you
> haven't installed it. You don't need it. Anywhere this README says `task <name>`, you can:
> - run the plain command shown next to it, **or**
> - run `wails3 task <name>` instead (the Wails CLI you already have includes a Task runner), **or**
> - install Task once: `winget install Task.Task` (or `go install github.com/go-task/task/v3/cmd/task@latest`).

### C toolchain (cgo) — required

Audio capture uses miniaudio via cgo, so a C compiler must be on `PATH`:

- **Windows:** install [Zig](https://ziglang.org/download/) and set `CC="zig cc"`,
  **or** install mingw-w64 (e.g. via [MSYS2](https://www.msys2.org/) or
  [w64devkit](https://github.com/skeeto/w64devkit)). Verify with `gcc --version`.
- **macOS:** `xcode-select --install` (Clang).
- **Linux:** `gcc` + ALSA/PipeWire dev headers (e.g. `sudo apt install build-essential libasound2-dev`).

---

## Bundled transcription engine (whisper.cpp)

The whisper binaries and model are **large and not committed** (see `.gitignore`),
and the app does **not** auto-download them. Fetch them with the setup script:

```powershell
# Run from the repo root in PowerShell. This is all `task setup:whisper` does:
pwsh -NoProfile -ExecutionPolicy Bypass -File ./scripts/setup-whisper.ps1

# Equivalent shortcuts (only if you have the runners): task setup:whisper  /  wails3 task setup:whisper

# Options (smaller/faster model, or a specific engine build):
pwsh ./scripts/setup-whisper.ps1 -Model ggml-base.en.bin -Variant blas
```

This places everything where Parley looks:

```
resources/whisper/bin/Release/whisper-server.exe        # CPU fallback + DLLs
resources/whisper/bin/cuda/Release/whisper-server.exe   # NVIDIA CUDA + DLLs
resources/whisper/models/ggml-small.en-q5_1.bin    # default model
```

If a corporate proxy blocks the download (Hugging Face / GitHub), the script prints
the exact URL and target path so you can drop the files in manually — or skip the
bundled engine and set a **remote transcription URL** in Settings (see below).

> **Sources:** binaries come from
> [ggml-org/whisper.cpp releases](https://github.com/ggml-org/whisper.cpp/releases);
> models from [Hugging Face: `ggerganov/whisper.cpp`](https://huggingface.co/ggerganov/whisper.cpp/tree/main).
> (The GitHub org is `ggml-org`, but the model files live under the original
> author's Hugging Face namespace `ggerganov` — pointing at `ggml-org` on Hugging
> Face returns a misleading **401**, since HF answers 401 for repos that don't exist.)

### Choosing a model (CPU-friendly)

The default — **`ggml-small.en-q5_1.bin`** (~182 MB) — is chosen for a capable
enterprise laptop that needs to stay responsive for other work: it is quantized
(low RAM/CPU), transcribes a 5-second chunk in well under a second, and is markedly
better than `base` at names, acronyms, and jargon — exactly what meetings are full
of. whisper only works in short bursts per audio chunk, so even this leaves plenty
of headroom. Tune in **Settings → Transcription**:

On Windows, the packaged app automatically uses the CUDA engine when a working
NVIDIA GPU is detected. If CUDA cannot start, Parley retries with the bundled CPU
engine so transcription remains available. Other GPU backends are not yet bundled.

| Model file | Size | Speed | Accuracy | When to pick it |
|------------|------|-------|----------|-----------------|
| `ggml-base.en.bin` | ~142 MB | fastest | good | older/under-powered machine |
| `ggml-small.en-q5_1.bin` *(default)* | ~182 MB | fast | better | the balanced default |
| `ggml-small.en.bin` | ~466 MB | fast | better | unquantized small |
| `ggml-large-v3-turbo-q5_0.bin` | ~547 MB | moderate | best | accuracy-first, CPU to spare |

`large-v3-turbo` is the modern speed/quality sweet spot at the top end (≈8× faster
decoding than `large-v3`); pick it if accuracy matters more than leaving the CPU idle.
Drop the file in `resources/whisper/models/` and set its filename in Settings, or pass
it to the script: `pwsh ./scripts/setup-whisper.ps1 -Model ggml-large-v3-turbo-q5_0.bin`.

### Or: use a remote transcription server

If you'd rather not transcribe on this machine, run a `whisper.cpp` server elsewhere
and set **Settings → Transcription → Remote transcription URL** (e.g.
`http://192.168.1.10:8765`). When set, Parley skips the bundled engine entirely.

> ⚠️ **Platform note:** the bundled-engine path is currently hard-coded to the Windows
> layout (`bin/Release/whisper-server.exe`). On macOS/Linux, use the **remote URL**
> option until the cross-platform launcher lands (see *Roadmap*).

---

## Run & build

```bash
# Development (hot reload). Uses a Vite port to avoid clashing with other dev servers.
task dev        # no Task? → wails3 dev -config ./build/config.yml -port 9245

# Production build → ./bin
task build      # no Task? → wails3 task build

# Package an installer
task package    # no Task? → wails3 task package
```

> The `build`/`package` recipes set important flags (e.g. `-H windowsgui`, so no
> console window appears). Prefer `task …` or `wails3 task …` over a bare
> `wails3 build` so those flags are applied — or install Task (see Prerequisites).

When packaging, make sure the `resources/whisper/` folder ships **next to the
executable** (Parley searches the working dir and the exe's directory + parents).

---

## Using Parley

1. **Audio sources** (sliders icon): pick your mic (label **Me**) and the system
   output to capture (label **Others**). For a single in-person mic where speakers
   can't be separated, choose **In-person / mixed** (labelled *Room*).
2. **Meeting context** (notebook icon): paste an agenda / attendees / notes, or import
   a `.txt`. Save it as a profile and mark it active to ground the analysis.
3. **Settings** (gear icon): save one **LLM connection** per provider (name, base
   URL, model, optional API key) — a local llama-server / LM Studio / Ollama, or a
   cloud URL. Mark one **active** (★), **Test** each, and set the analysis interval
   and transcription options. Switch which connection a meeting uses from the
   **LLM connection dropdown in the header** (before you start the meeting).
4. **Start listening.** The transcript streams on the left; topic / assertions / past
   topics / suggestions populate on the right.

### Live context injection

While a meeting is running, use the input at the bottom of the transcript to nudge the
assistant. Pick a scope:

- **This topic** — corrects the immediate discussion (e.g. *"this is about margins, not
  revenue"*) and **expires automatically when the topic changes**, so a correction can
  never bleed stale info into the next topic.
- **Whole meeting** — standing facts that apply all session (e.g. *"the client is Acme —
  A-C-M-E"*, name spellings, themes).

Active notes appear as chips; whole-meeting notes persist, topic notes drop on a topic
change.

### Saving, loading & resuming

Every meeting is **auto-saved continuously** (transcript, topics, assertions,
suggestions, and live notes) — a crash or close never loses your data. Open **Saved
meetings** (history icon) to:

- **View** a past meeting read-only, or
- **Resume** it — Parley reloads its state and continues recording into the same
  meeting, so a conversation can span several sittings.

Audio is recorded per source under your app-data `recordings/session-<id>/` folder.

---

## Troubleshooting

- **"The local transcription engine isn't installed" on Start.** You haven't fetched
  the whisper engine yet — run **`task setup:whisper`** (or `scripts/setup-whisper.ps1`),
  or set a remote transcription URL in Settings. Parley shows the reason in a red banner
  and writes full details to **`parley.log`** in your app-data folder (Windows:
  `%AppData%\Parley\`). For a packaged build, the `resources/whisper/` folder must sit
  next to the `.exe`.
- **"No mic" with a mic selected.** The badge now reflects whether a microphone source
  actually started. If it still says *No mic*, that device failed to open (wrong device,
  in use, or unsupported format) — check `parley.log` and try another device.
- **LLM "context deadline exceeded".** The endpoint didn't answer in time — check the
  URL/port, that the server is up, and (for local servers) that the model finished
  loading. The Settings dialog now explains common failures.
- **App crashes when dragging the window between monitors.** This was a WebView2 bug:
  while the window is moving to another monitor the WebView2 controller is briefly in a
  transitional state, and `Chromium.Focus()` called `controller.MoveFocus()`
  unconditionally — which returns `ERROR_INVALID_STATE` (`0x8007139F`). Older Wails
  builds treated that transient COM error as fatal (`os.Exit(1)`), taking the whole
  process down. (It reproduces on same-DPI setups too, not only across a DPI boundary.)
  Note that Parley's panic logging could never catch this — `os.Exit(1)` bypasses
  deferred funcs and `recover()`, which is why the crash left no trace in `parley.log`.

  The crash fix is upstream issue
  [#5650](https://github.com/wailsapp/wails/issues/5650) /
  [#5568](https://github.com/wailsapp/wails/pull/5568), shipped in `webview2 v1.0.25`
  (first bundled in Wails `v3.0.0-alpha2.106`). A follow-on mixed-DPI bug where content
  shrinks then disappears after a cross-DPI drag
  ([#5677](https://github.com/wailsapp/wails/issues/5677) /
  [#5689](https://github.com/wailsapp/wails/pull/5689)) was fixed in
  `v3.0.0-alpha2.109`. This repo now targets **`v3.0.0-alpha2.109`** (pins
  `webview2 v1.0.27`), which carries both fixes. If you still see the crash:
  1. **Rebuild clean** so the fixed library is actually linked:
     `go clean -cache && rm -rf bin && wails3 build`. Confirm the resolved versions with
     `go list -m github.com/wailsapp/wails/v3` (expect `…alpha2.109`) and
     `go list -m github.com/wailsapp/wails/webview2` (expect `v1.0.27`, must be ≥ v1.0.25).
  2. **Update the WebView2 Runtime** on the machine (old Evergreen runtimes mishandle
     the DPI transition).
  3. If it persists, it's likely a different un-converted WebView2 call site — grab the
     stack from **`parley.log`** (`%AppData%\Parley\`); Parley now records panics with a
     full goroutine trace and routes crash output there, so the failing call is visible.

---

## Data & privacy

- SQLite database + log + recordings live under your OS app-config dir (`%AppData%\Parley`
  on Windows). Each LLM connection's API key is stored in the **OS keychain** (one
  entry per connection), never in the database.
- No telemetry. Transcription is local unless you opt into a remote STT URL; the LLM is
  whatever endpoint you configure (point it at localhost to stay fully offline).

---

## Roadmap (high level)

See `docs/PLAN.md` for phase status and `docs/POLISH-BACKLOG.md` for deferred polish.
Next up: cross-platform bundled-engine launcher (macOS/Linux), session full-text search
and export, whisper model picker with on-demand download, and GPU acceleration.
