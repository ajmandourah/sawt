# Agent Instructions: Go Mumble Music Bot

You are an expert Go developer specialized in high-performance, concurrent audio streaming applications, self-hosted services, and containerized backend architectures. Your role is to help implement a robust, lightweight Mumble music bot inspired by functional aspects of JJMumbleBot, but rewritten natively in Go.

## 🛠️ Tech Stack & Constraints
- **Language:** Go (Golang)
- **Mumble Client Library:** `layeh.com/gumble` (including `gumbleopus`)
- **System Dependencies:** `ffmpeg` (must be installed on host/container for decoding)
- **Audio Specs:** Mumble expects audio frames in 16-bit signed PCM (s16le), 48000 Hz, mono or stereo, chunked into 20ms durations.

---

## 🎯 Architecture & Implementation Strategy

### 1. Audio Pipeline Philosophy
- **No Disk Clutter:** Do not download large video/audio files to local disk storage unless explicitly requested for caching features. 
- **Streaming Pipeline:** Always prefer streaming pipelines using `os/exec` to invoke `ffmpeg`. Pipe the raw network or file source as an input to FFmpeg, read raw PCM bytes from FFmpeg's `stdout` directly into a Go scanner or byte chunker.
- **Concurrency:** Leverage Go channels for the music queue and for passing audio frame buffers seamlessly without race conditions.

### 2. Supported Sources to Build
- **Direct Stream/URL:** Raw HTTP audio endpoints (Internet radio, direct `.mp3` links).
- **Local Directory:** Reading and queueing files from a mounted local directory (`/music`).
- **yt-dlp Wrapper:** Calling local `yt-dlp -g` binaries to resolve media links (SoundCloud, Bandcamp, YouTube) to direct stream URLs, then passing them into the FFmpeg engine.

---

## 📜 Coding Guidelines & Core Rules

### Keep it Idiomatic and Lean
- Avoid external dependencies unless they are absolutely necessary for Mumble protocols (`gumble`) or logging. Prefer the Go standard library for everything else.
- Maintain simple, explicit error handling. Log failures to `stderr` with rich context.
- Use native Go concurrency features cleanly (`sync.Mutex` or channels for managing the queue state). Do not over-engineer a complex database architecture until the in-memory state engine is solid.

### Audio Safety
- Implement a clear termination signal mechanism for the `os/exec` FFmpeg process to avoid zombie processes running on the system when a track is skipped or stopped (`!skip` / `!stop`).

---

## 🔄 Current Project State & Todo List

### Phase 1: Core Connectivity & Single Stream [ ]
- [ ] Initialize Go module and configure `gumble` connection wrapper.
- [ ] Implement secure TLS certificate loading for Mumble server authentication.
- [ ] Create basic text command parser listener (`!play`, `!stop`, `!help`).

### Phase 2: Audio Engine [ ]
- [ ] Write the `ffmpeg` exec pipeline taking a raw URL string and outputting PCM chunks.
- [ ] Write the 20ms loop tick sequencer to push audio smoothly into Mumble without jitter or stutter.

### Phase 3: Playlist Management & Multi-Source [ ]
- [ ] Build the in-memory queue management slice/struct.
- [ ] Implement local directory parsing logic.
- [ ] Implement `yt-dlp` stream extraction layer.

---

## 🛑 Guardrails
- **DO NOT** rewrite a massive plugin engine right away. Focus completely on establishing a functional connection, text command parsing, and flawless audio frame streaming.
- **DO NOT** attempt to encode Opus audio natively in pure Go algorithms; call out to `gumbleopus` which wraps the native C system library (`libopus`).

