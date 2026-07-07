# Sawt (صوت) — Architectural Blueprint

> A highly optimized, self-hosted Mumble music bot written in Go.
> This document establishes the technical foundation before any implementation begins.

---

## Table of Contents

1. [Project File Layout](#1-project-file-layout)
2. [Audio Pipeline & Memory Architecture](#2-audio-pipeline--memory-architecture)
3. [Concurrency & State Management](#3-concurrency--state-management)
4. [Multi-Source Stream Resolution Strategy](#4-multi-source-stream-resolution-strategy)
5. [Explicit Execution Phases](#5-explicit-execution-phases)

---

## 1. Project File Layout

### Guiding Principles

- **Flat but modular.** No deep nesting. Each package has a single clear responsibility.
- **No dependency injection frameworks.** Idiomatic Go — pass what you need, compose what you build.
- **Configuration at the top.** One config struct flows down; no global state.

### Proposed Structure

```
sawt/
├── cmd/
│   └── sawt/
│       └── main.go              # Entry point: wire config, start bot
├── internal/
│   ├── config/
│   │   └── config.go            # Config struct + YAML/flag loading
│   ├── mumble/
│   │   └── client.go            # gumble connection wrapper, auth, TLS, reconnect
│   ├── audio/
│   │   ├── engine.go            # FFmpeg exec pipeline, stdout reader, frame slicer
│   │   ├── frames.go            # 20ms PCM frame calculation, chunking logic
│   │   └── encoder.go           # gumbleopus encoding wrapper (s16le → Opus)
│   ├── queue/
│   │   └── queue.go             # In-memory playback queue, state machine (Play/Pause/Skip/Stop)
│   ├── source/
│   │   ├── resolver.go          # SourceResolver interface + registry
│   │   ├── direct.go            # Direct HTTP / file:// streams
│   │   ├── local.go             # Local directory scanner
│   │   └── ytdlp.go             # yt-dlp wrapper (URL → direct stream URL)
│   └── command/
│       └── handler.go           # Text command parser (!play, !skip, !stop, !queue, !help)
├── music/                       # Mount point for local files (gitignored)
├── go.mod
├── go.sum
├── Dockerfile
├── docker-compose.yml
├── ARCHITECTURE.md              # This file
└── AGENTS.md
```

### Package Responsibilities

| Package | Responsibility |
|---------|---------------|
| `cmd/sawt` | Bootstrap only — load config, create dependencies, call `bot.Run()` |
| `internal/config` | Parse CLI flags + optional YAML config file |
| `internal/mumble` | gumble connection lifecycle, TLS cert loading, channel joining, text message dispatch |
| `internal/audio` | FFmpeg process lifecycle, stdout reading, PCM frame slicing, Opus encoding |
| `internal/queue` | Thread-safe queue management, playback state machine |
| `internal/source` | Source resolution: turn any user input into a playable stream URL or file path |
| `internal/command` | Chat command parsing, help text, user-facing feedback |

### What Stays Flat

- No `pkg/` directory — this is a single binary, not a library.
- No `adapter/`, `repository/`, or `usecase/` layers — we are not building a hexagonal architecture for a music bot.
- Interfaces are defined where they are consumed, not in separate packages.

---

## 2. Audio Pipeline & Memory Architecture

### 2.1 FFmpeg Execution Strategy

We use `os/exec.Command` to spawn FFmpeg as a child process. FFmpeg reads the source (URL or file) and writes raw PCM to `stdout`, which Go reads via a pipe.

```
[Source] → FFmpeg (child process) → stdout pipe → Go byte reader → [Frame Slicer] → [Opus Encoder] → Mumble
```

**FFmpeg Arguments Template:**

```
ffmpeg -i <input> \
  -f s16le \
  -acodec pcm_s16le \
  -ar 48000 \
  -ac 2 \
  -loglevel error \
  -
```

Key flags:

- `-f s16le` — raw 16-bit signed little-endian PCM output
- `-ar 48000` — Mumble requires exactly 48 kHz sample rate
- `-ac 2` — stereo (Mumble handles stereo Opus natively)
- `-loglevel error` — suppress FFmpeg info/warning noise; only surface actual errors
- `-` — write to stdout (pipe)

### 2.2 Frame Size Mathematics

Mumble expects audio frames at **20ms intervals** in Opus encoding.

```
Bytes per 20ms frame (s16le, 48000Hz, stereo):
  = sampleRate × channels × bitsPerSample/8 × durationSeconds
  = 48000 × 2 × 2 × 0.020
  = 3,840 bytes per frame
```

This is a **fixed constant** — every frame is exactly 3,840 bytes of raw PCM before Opus encoding.

### 2.3 Buffer Strategy

```
┌─────────────────────────────────────────────┐
│  FFmpeg stdout pipe                         │
│  (unbounded stream of PCM bytes)            │
└──────────────┬──────────────────────────────┘
               │ bufio.Reader (32KB buffer)
               ▼
┌─────────────────────────────────────────────┐
│  Frame Slicer                               │
│  Reads exactly 3840 bytes per tick          │
│  Blocks until full frame is available       │
└──────────────┬──────────────────────────────┘
               │ chan [3840]byte (buffered: 4)
               ▼
┌─────────────────────────────────────────────┐
│  Opus Encoder (gumbleopus)                  │
│  PCM → Opus packet                          │
└──────────────┬──────────────────────────────┘
               │ gumble.Client.SendAudio()
               ▼
┌─────────────────────────────────────────────┐
│  Mumble Server                              │
└─────────────────────────────────────────────┘
```

**Buffer sizing rationale:**

- `bufio.Reader` wraps the pipe with a **32KB internal buffer**. This absorbs FFmpeg's bursty output without excessive syscalls.
- The frame slicer reads **exactly 3,840 bytes** per frame using `io.ReadFull`. This blocks until a complete frame is available — no partial frames, no race conditions.
- The channel between slicer and encoder is **buffered to 4 frames** (~80ms of audio). This provides a small decoupling buffer so encoding latency doesn't block the reader.

**Memory footprint:** At steady state, we hold ~32KB (bufio) + ~15KB (channel buffer) + ~4KB (current frame) = **~51KB of audio data in memory**. The FFmpeg process holds its own internal buffers (typically < 1MB). Total memory for the audio pipeline: **under 2MB**.

### 2.4 Opus Encoding

We use `gumbleopus` which wraps the system's `libopus` C library via CGO. The flow:

1. Take 3,840 bytes of raw s16le PCM.
2. Call `gumbleopus.Encode(pcmBytes)` → returns `[]byte` Opus packet.
3. Call `client.SendAudio(opusPacket)` to push to Mumble.

The encoder runs in the **same goroutine** as the frame sender (no extra goroutine needed). The 20ms tick is enforced by `time.NewTicker(20 * time.Millisecond)`.

### 2.5 Ticker Synchronization

```
for range ticker.C:  // fires every 20ms
    select:
    case frame := <-pcmChannel:
        opus := encoder.Encode(frame)
        client.SendAudio(opus)
    case <-stopChannel:
        ticker.Stop()
        ffmpegProcess.Kill()
        return
```

The ticker drives the rhythm. If the PCM channel is empty (e.g., FFmpeg hasn't produced data yet), we send silence. If `stopChannel` fires, we tear down immediately.

---

## 3. Concurrency & State Management

### 3.1 Playback State Machine

```
                ┌───────┐
   !play ──────▶│  PLAY  │◀──── !resume
                │       │
   !pause ─────▶│ PAUSE  │
                │       │
   !stop ──────▶│  STOP  │
                │       │
   !skip ──────▶│  STOP  │ ──(auto)──▶ PLAY (next track)
                └───────┘
```

States: `Idle`, `Playing`, `Paused`, `Stopping`

### 3.2 Queue Design

```go
type Queue struct {
    mu      sync.Mutex
    items   []*Track       // ordered slice
    current *Track         // currently playing
    pos     int            // position in items
}

type Track struct {
    Title   string
    Source  string          // resolved URL or file path
    SourceType SourceType   // direct, local, ytdlp
    RequestedBy string     // Mumble username
    Duration  time.Duration // if known
}
```

**Why a slice with mutex, not channels for the queue:**

- The queue needs random access (peek, insert at position, clear).
- Channels are FIFO streams — they don't support "what's next?" queries without draining.
- A mutex-protected slice is simple, fast, and gives us full CRUD.
- The **playback pipeline** uses channels (PCM frames, stop signals).
- The **queue data structure** uses mutexes (state mutations).

### 3.3 FFmpeg Process Lifecycle & Zombie Prevention

**Problem:** When `!skip` or `!stop` fires, the FFmpeg process must terminate instantly. Simply calling `process.Kill()` is not enough — we must also close the stdout pipe reader to unblock any goroutine reading from it.

**Solution: Three-pronged teardown:**

1. **Signal the stop channel** — unblocks the 20ms ticker loop via `select`.
2. **Kill the FFmpeg process** — `process.Kill()` sends SIGKILL.
3. **Wait for process exit** — `process.Wait()` reaps the zombie.

```
Teardown sequence (all in one goroutine):
  1. close(stopChannel)       // unblocks ticker loop
  2. ffmpegProc.Kill()        // kills child process
  3. ffmpegProc.Wait()        // reaps zombie, closes stdout pipe
  4. close(pcmChannel)        // signals encoder goroutine to exit
  5. wait for encoder goroutine (via sync.WaitGroup)
```

**Goroutine ownership:**

- The `Engine` struct owns the FFmpeg process and its goroutines.
- A `sync.WaitGroup` tracks the reader goroutine and encoder goroutine.
- `Engine.Stop()` is blocking — it does not return until all goroutines are joined and the process is reaped.
- The caller (queue manager) calls `Stop()` and then immediately starts the next track.

### 3.4 Command → Queue → Engine Flow

```
User types "!play <url>" in Mumble
        │
        ▼
┌──────────────────┐
│  command/handler  │  Parse command, validate args
└────────┬─────────┘
         │ resolved Track
         ▼
┌──────────────────┐
│   queue/queue    │  Enqueue (or play immediately if idle)
└────────┬─────────┘
         │ On state change → PLAY
         ▼
┌──────────────────┐
│   audio/engine   │  Start FFmpeg, start ticker, stream audio
└────────┬─────────┘
         │ On track complete / skip / stop
         ▼
┌──────────────────┐
│   queue/queue    │  Pop next track, repeat
└──────────────────┘
```

The queue manager is the **orchestrator**. It owns the state machine and delegates playback to the audio engine. The engine is stateless — it plays what it's given and reports when done.

---

## 4. Multi-Source Stream Resolution Strategy

### 4.1 SourceResolver Interface

```go
type SourceResolver interface {
    CanHandle(input string) bool    // Can this resolver handle the input?
    Resolve(ctx context.Context, input string) (*ResolvedSource, error)
}

type ResolvedSource struct {
    URL    string    // Final playable URL or file path
    Title  string    // Human-readable title
    Type   SourceType // direct-http, file-local, ytdlp-extracted
}
```

### 4.2 Resolution Chain

Resolvers are tried in order. First match wins:

```
User Input → [IsLocalFile?] → [IsURL?] → [Try yt-dlp]
               │                │              │
               ▼                ▼              ▼
           file://path    direct HTTP    yt-dlp -g <url>
           (local.go)    (direct.go)   (ytdlp.go)
```

### 4.3 Resolver Details

#### A. Local File Resolver (`local.go`)

- **Detection:** `filepath.Abs(input)` exists and file is readable.
- **Supported formats:** Anything FFmpeg can read (mp3, flac, wav, ogg, m4a, etc.).
- **Output:** `file:///absolute/path/to/file` (FFmpeg reads local files directly).
- **Directory scanning:** When user provides a directory path, recursively find all audio files and enqueue as individual tracks.

#### B. Direct HTTP Stream Resolver (`direct.go`)

- **Detection:** Input starts with `http://` or `https://`.
- **Strategy:** Pass URL directly to FFmpeg. FFmpeg handles HTTP streaming natively.
- **Internet radio:** Works transparently — FFmpeg keeps the HTTP connection alive.
- **Edge case:** Some URLs are web pages, not streams. We do NOT fetch and parse HTML. If FFmpeg cannot decode the stream, we surface the error to the user.

#### C. yt-dlp Wrapper (`ytdlp.go`)

- **Detection:** Fallback for any URL that the direct resolver cannot handle, or explicit `!ytpl` command.
- **Mechanism:**

  ```
  yt-dlp -g --no-playlist <url>
  ```

  - `-g` → output the direct video/audio URL
  - `--no-playlist` → don't resolve entire playlists (user can use `!ytpl` for that)
- **FFmpeg receives:** The direct URL output by yt-dlp (often a DASH or HLS fragment URL).
- **Process model:** yt-dlp runs as a **short-lived child process** (resolve → get URL → exit). It does NOT stream audio. It only resolves the URL. FFmpeg then streams from the resolved URL.
- **Caching consideration:** yt-dlp URLs can expire (especially YouTube DASH URLs). We resolve fresh for each track. No URL caching in MVP.

### 4.4 Resolution Error Handling

| Scenario | Behavior |
|----------|----------|
| File not found | Tell user "File not found: {path}" |
| HTTP 404 / unreachable | Tell user "Cannot reach stream: {url}" |
| yt-dlp not installed | Tell user "yt-dlp is not installed on this system" |
| yt-dlp cannot resolve | Tell user "Could not resolve: {url} (yt-dlp failed)" |
| Unsupported format | Tell user with FFmpeg's error message |

All errors are reported back to the user via Mumble text chat.

---

## 5. Explicit Execution Phases

### Phase 1: Skeleton & Connection (Week 1)

**Goal:** Bot connects to Mumble, stays connected, and responds to `!help`.

| Step | Deliverable | Test |
|------|-----------|------|
| 1.1 | `go mod init`, `config/config.go` with server, username, password, channel | `go build ./...` compiles |
| 1.2 | `mumble/client.go` — connect, authenticate, join channel | Bot appears in Mumble |
| 1.3 | Reconnect logic (exponential backoff on disconnect) | Kill connection → bot reconnects |
| 1.4 | `command/handler.go` — `!help` command, text message listener | `!help` returns formatted help text |
| 1.5 | TLS certificate loading for secure servers | Connect to TLS-enabled server |

**Milestone:** Bot is visible in Mumble and responds to `!help`.

---

### Phase 2: Audio Engine — Local Files (Week 2)

**Goal:** Play a local audio file through Mumble with clean audio.

| Step | Deliverable | Test |
|------|-----------|------|
| 2.1 | `audio/frames.go` — frame size constants, PCM byte math | Unit test: 3840 bytes = 20ms frame |
| 2.2 | `audio/engine.go` — FFmpeg exec, stdout pipe reader | FFmpeg runs, stdout has PCM data |
| 2.3 | Frame slicer — read exactly 3840 bytes per tick | Unit test: slice stream into frames |
| 2.4 | `audio/encoder.go` — gumbleopus PCM → Opus encoding | Unit test: encode silence frame |
| 2.5 | 20ms ticker loop — SendAudio to Mumble | Play local .wav → hear audio in Mumble |
| 2.6 | `!stop` command — kill FFmpeg, stop audio | `!stop` → audio cuts within 20ms |
| 2.7 | Track completion detection — FFmpeg exits → play next | Queue 2 files → both play sequentially |

**Milestone:** Local audio files play cleanly through Mumble.

---

### Phase 3: Queue & Playback Controls (Week 3)

**Goal:** Full queue management with skip, pause, and state tracking.

| Step | Deliverable | Test |
|------|-----------|------|
| 3.1 | `queue/queue.go` — mutex-protected queue with Enqueue/Dequeue/Peek | Unit tests for queue operations |
| 3.2 | `!play <file>` — enqueue and auto-play if idle | Enqueue 3 files, all play in order |
| 3.3 | `!skip` — stop current, start next | Skip mid-track → next starts within 100ms |
| 3.4 | `!pause` / `!resume` — pause ticker, resume ticker | Pause → silence → resume → audio continues |
| 3.5 | `!queue` — display current queue with track titles | `!queue` shows numbered list |
| 3.6 | `!nowplaying` — show current track info | Shows title, source, requester |

**Milestone:** Full playback control with queue management.

---

### Phase 4: Multi-Source Resolution (Week 4)

**Goal:** Handle HTTP streams, local directories, and yt-dlp sources.

| Step | Deliverable | Test |
|------|-----------|------|
| 4.1 | `source/resolver.go` — resolver registry and chain | Unit test: chain selects correct resolver |
| 4.2 | `source/direct.go` — HTTP stream passthrough | Play internet radio URL |
| 4.3 | `source/local.go` — file validation + directory scanning | `!play /music/` → enqueues all files |
| 4.4 | `source/ytdlp.go` — yt-dlp wrapper for URL extraction | `!play https://youtube.com/...` → resolves and plays |
| 4.5 | Error handling — user-friendly messages for failed sources | Bad URL → clear error message in chat |

**Milestone:** Bot can play from any source type.

---

### Phase 5: Polish & Containerization (Week 5)

**Goal:** Production-ready deployment with Docker.

| Step | Deliverable | Test |
|------|-----------|------|
| 5.1 | `Dockerfile` — multi-stage build with FFmpeg + libopus | `docker build` produces < 100MB image |
| 5.2 | `docker-compose.yml` — config volume, music volume | `docker compose up` → bot connects and plays |
| 5.3 | Graceful shutdown — SIGTERM → finish current track → exit | `docker stop` → clean shutdown |
| 5.4 | Logging — structured logging with context | Logs show track transitions, errors |
| 5.5 | Config file support — YAML config as alternative to flags | `sawt --config sawt.yml` works |

**Milestone:** Bot runs in production with Docker.

---

## Appendix A: Data Flow Diagram (Complete System)

```
┌─────────────────────────────────────────────────────────────────┐
│                        Sawt Bot Process                         │
│                                                                 │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐                  │
│  │  Mumble   │    │ Command   │    │  Queue   │                  │
│  │  Client   │◄──▶│ Handler   │───▶│ Manager  │                  │
│  │ (gumble)  │    │          │    │          │                  │
│  └─────┬─────┘    └──────────┘    └────┬─────┘                  │
│        │                               │                        │
│        │ SendAudio()                   │ Start(track)           │
│        ▼                               ▼                        │
│  ┌──────────────────────────────────────────────┐              │
│  │              Audio Engine                     │              │
│  │                                              │              │
│  │  ┌──────────┐  stdout   ┌──────────┐        │              │
│  │  │  FFmpeg   │─────────▶│  Reader  │        │              │
│  │  │ (child)   │  pipe    │ (bufio)  │        │              │
│  │  └──────────┘           └────┬─────┘        │              │
│  │                              │ 3840B        │              │
│  │                              ▼              │              │
│  │                       ┌──────────┐          │              │
│  │                       │ Slicer   │          │              │
│  │                       └────┬─────┘          │              │
│  │                              │ chan         │              │
│  │                              ▼              │              │
│  │                       ┌──────────┐          │              │
│  │                       │  Opus    │──────────┤              │
│  │                       │ Encoder  │  Send    │              │
│  │                       └──────────┘          │              │
│  └──────────────────────────────────────────────┘              │
│                                                                 │
│  ┌──────────────────────────────────────────────┐              │
│  │           Source Resolvers                    │              │
│  │  ┌────────┐ ┌────────┐ ┌──────────┐          │              │
│  │  │ Local  │ │ Direct │ │  yt-dlp   │          │              │
│  │  │ File   │ │ HTTP   │ │ Wrapper   │          │              │
│  │  └────────┘ └────────┘ └──────────┘          │              │
│  └──────────────────────────────────────────────┘              │
└─────────────────────────────────────────────────────────────────┘
        │                                    │
        ▼                                    ▼
   Mumble Server                    External Sources
   (Voice + Text)              (Files, HTTP, YouTube)
```

## Appendix B: Key Constants

| Constant | Value | Purpose |
|----------|-------|---------|
| `SampleRate` | 48000 Hz | Mumble audio sample rate |
| `Channels` | 2 | Stereo |
| `BitsPerSample` | 16 | 16-bit signed PCM |
| `FrameDuration` | 20ms | Mumble frame interval |
| `BytesPerFrame` | 3840 | 48000 × 2 × 2 × 0.020 |
| `BufioBufferSize` | 32768 (32KB) | Pipe reader buffer |
| `ChannelBuffer` | 4 frames | Decoupling buffer (~80ms) |
| `ReconnectBaseDelay` | 1s | Exponential backoff start |
| `ReconnectMaxDelay` | 60s | Exponential backoff cap |

## Appendix C: External Dependencies

| Dependency | Purpose | Why |
|------------|---------|-----|
| `layeh.com/gumble` | Mumble protocol client | Required for Mumble connection |
| `gopkg.in/yaml.v3` | Config file parsing | Standard YAML support |
| System: `ffmpeg` | Audio decoding | Industry-standard decoder |
| System: `libopus` | Opus encoding (via CGO) | Mumble requires Opus |
| System: `yt-dlp` | URL resolution (optional) | YouTube, SoundCloud, etc. |

**No other external Go dependencies.** Standard library for everything else (HTTP, exec, filepath, sync, time, log).

---

*This blueprint is a living document. It will be updated as implementation reveals new requirements or architectural refinements.*
