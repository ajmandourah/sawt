# Debug Logging Audit — Sawt Go Mumble Music Bot

## Summary

Scanned all `.go` files under `cmd/` and `internal/` (17 files). Found **~85 `log.Printf` calls** across the codebase. No `fmt.Println`/`fmt.Printf` debug usage found. No `// DEBUG` or `// TODO: remove log` comments found.

One file is **entirely a development debug tool** and should be removed before production. The jitter buffer logs **every single audio packet** (~4800 logs/sec at 48kHz stereo), which would be catastrophic in production.

---

## Findings by Severity

### 🔴 CRITICAL — Remove Entire File (Dev-Only Tool)

| File | Lines | Description |
|------|-------|-------------|
| `cmd/debug-mumble/main.go` | 1–94 | Entire file is a hardcoded debug tool that connects to `127.0.0.1:64738` as "DebugBot", lists channels, creates "Music" channel, and quits. Uses `log.Printf` on lines 23–94 for verbose connection debugging. **Not part of production binary.** |

---

### 🟠 HIGH — Per-Packet/Per-Frame Spam (Will Flood Logs)

| File | Line | Statement | Reason |
|------|------|-----------|--------|
| `internal/audio/jitter.go` | 80 | `log.Printf("JitterBuffer: added packet %d, heap size=%d", seq, jb.heap.Len())` | **Every audio packet logged** — at 48kHz stereo that's ~9600 log lines/sec. |
| `internal/audio/jitter.go` | 116 | `log.Printf("JitterBuffer: woke up, heap size=%d", jb.heap.Len())` | Every cond.Wait wakeup logged. |
| `internal/audio/jitter.go` | 123 | `log.Printf("JitterBuffer: skipping old packet %d (expected %d)", pkt.Sequence, jb.seq)` | Per-packet skip logging. |
| `internal/audio/jitter.go` | 132 | `log.Printf("JitterBuffer: gap detected, expected %d, got %d", jb.seq, pkt.Sequence)` | Per-gap logging. |
| `internal/audio/jitter.go` | 147 | `log.Printf("JitterBuffer: sending packet %d to sink", pkt.Sequence)` | Every packet sent to sink logged. |
| `internal/audio/jitter.go` | 197 | `log.Printf("JitterSink: SendAudio called, samples=%d", len(samples))` | Every SendAudio call logged. |

Also in `internal/audio/jitter.go`, lines 88, 98, 102, 111, 152 are lifecycle debug prints (start/stop). These are lower volume but still noise.

---

### 🟡 MEDIUM — Verbose Operational Logging (Acceptable in Dev, Spam in Prod)

| File | Line | Statement | Reason |
|------|------|-----------|--------|
| `internal/source/resolver.go` | 71 | `log.Printf("source: resolving %s via %T", input, r)` | Per-resolution verbose logging with Go type name. |
| `internal/source/resolver.go` | 79 | `log.Printf("source: %s failed for %s: %v", ...)` | Per-failure logging. |
| `internal/source/resolver.go` | 86 | `log.Printf("source: resolved %s → %s", input, src.Title)` | Per-success logging. |
| `internal/source/resolver.go` | 104 | `log.Printf("source: retry %d/%d for %s, waiting %v", ...)` | Retry attempt logging. |
| `internal/mumble/client.go` | 102 | `log.Printf("Creating stereo Opus encoder...")` | Per-connect attempt. |
| `internal/mumble/client.go` | 105 | `log.Printf("ERROR: Failed to create stereo Opus encoder: %v", err)` | OK as error, but verbose in reconnect loops. |
| `internal/mumble/client.go` | 109 | `log.Printf("Successfully created stereo Opus encoder")` | Unnecessary success log. |
| `internal/mumble/client.go` | 116 | `log.Printf("Mumble connected")` | OK for startup, spam on reconnects. |
| `internal/mumble/client.go` | 121 | `log.Printf("ERROR: stereoEnc is nil!")` | Internal state debug. |
| `internal/mumble/client.go` | 123 | `log.Printf("ERROR: stereoEnc.enc is nil (encoder creation failed)!")` | Internal state debug. |
| `internal/mumble/client.go` | 126 | `log.Printf("Re-applied stereo encoder after connect")` | Unnecessary success log. |
| `internal/mumble/client.go` | 142 | `log.Printf("Mumble disconnected: %v (%s)", e.Type, e.String)` | OK for disconnect events. |
| `internal/mumble/client.go` | 183 | `log.Printf("Reconnecting in %v...", delay)` | OK for reconnects. |
| `internal/mumble/client.go` | 188 | `log.Printf("Reconnected to Mumble")` | OK. |
| `internal/mumble/client.go` | 194 | `log.Printf("Reconnect failed: %v", err)` | OK as error. |
| `internal/mumble/client.go` | 209 | `log.Printf("Text from %s: %q", e.Sender.Name, e.Message)` | **Every text message** logged — could be very frequent. |
| `internal/mumble/client.go` | 232 | `log.Printf("JoinChannel: client is nil")` | OK as error path. |
| `internal/mumble/client.go` | 236 | `log.Printf("Joining channel: %q", name)` | OK. |
| `internal/mumble/client.go` | 237 | `log.Printf("Self: session=%d name=%q channel=%q", ...)` | Verbose state dump. |
| `internal/mumble/client.go` | 241 | `log.Printf("Channel %q not found — staying in %s (ID=%d)", ...)` | OK as error/warning. |
| `internal/mumble/client.go` | 261 | `log.Printf("Sent Move to %s (ID=%d), waiting for server confirmation", ...)` | OK. |
| `internal/mumble/client.go` | 265 | `log.Printf("Joined channel: %s (ID=%d)", ch.Name, ch.ID)` | OK. |
| `internal/mumble/client.go` | 267 | `log.Printf("Timeout waiting for channel join confirmation to %s", name)` | OK as error. |
| `internal/mumble/client.go` | 301 | `log.Printf("Replying to %s: %s", user.Name, msg)` | OK. |
| `internal/mumble/client.go` | 309 | `log.Printf("OpenAudio: client is nil!")` | OK as error. |
| `internal/mumble/client.go` | 315 | `log.Printf("OpenAudio: channel opened")` | OK. |
| `internal/mumble/client.go` | 325 | `log.Printf("CloseAudio: channel closed")` | OK. |
| `internal/audio/engine.go` | 99 | `log.Printf("Engine: using jitter buffer with %dms delay", jitterDelayMs)` | OK for startup. |
| `internal/audio/engine.go` | 102 | `log.Printf("Engine: jitter buffer disabled")` | OK for startup. |
| `internal/audio/engine.go` | 162 | `log.Printf("yt-dlp downloading: %s", ytURL)` | OK per-track. |
| `internal/audio/engine.go` | 175 | `log.Printf("yt-dlp downloaded %d bytes", info.Size())` | OK per-track. |
| `internal/audio/engine.go` | 223 | `log.Printf("Engine: opening audio sink")` | OK per-track. |
| `internal/audio/engine.go` | 227 | `log.Printf("Engine: starting runLoop")` | OK per-track. |
| `internal/audio/engine.go` | 254 | `log.Printf("FFmpeg startup error: %s", errStr)` | OK as error. |
| `internal/audio/engine.go` | 287 | `log.Printf("Reader: stop requested, exiting")` | OK. |
| `internal/audio/engine.go` | 301 | `log.Printf("Reader: stop requested in frame extraction")` | OK. |
| `internal/audio/engine.go` | 312 | `log.Printf("Reader: stop requested in channel send")` | OK. |
| `internal/audio/engine.go` | 325 | `log.Printf("Reader: read error after %d bytes: %v", totalBytes, err)` | OK as error. |
| `internal/audio/engine.go` | 337 | `log.Printf("Playback stopped after %d frames", frameCount)` | OK. |
| `internal/audio/engine.go` | 344 | `log.Printf("Audio stream ended after %d frames (%.1fs)", ...)` | OK. |
| `internal/audio/engine.go` | 390 | `log.Printf("Kill FFmpeg: %v", err)` | OK as error. |
| `internal/command/handler.go` | 77 | `log.Printf("Command %q from %s: %s", action.Command, source.Name, action.Args)` | Every command logged — moderate volume, reasonable for audit. |

---

### 🟢 LOW — Reasonable Production Logging (Keep as-Is)

These are startup/shutdown/error messages that belong in production:

| File | Lines | Description |
|------|-------|-------------|
| `cmd/sawt/main.go` | 47–48 | Startup banner ("Sawt starting...", server/user/channel config) |
| `cmd/sawt/main.go` | 62 | Warning: yt-dlp not available |
| `cmd/sawt/main.go` | 64 | yt-dlp version info |
| `cmd/sawt/main.go` | 140 | WebUI server error |
| `cmd/sawt/main.go` | 144 | "Sawt v%s is online" banner |
| `cmd/sawt/main.go` | 214 | Shutdown message |
| `internal/api/server.go` | 94 | API static files error |
| `internal/api/server.go` | 156 | API server listening |
| `internal/api/server.go` | 474 | File upload start |
| `internal/api/server.go` | 482 | Upload successful |
| `internal/api/server.go` | 715 | Track resolution failure |
| `internal/api/store/store.go` | 75, 83, 89, 124, 174, 240, 252, 354, 366, 407, 416, 427 | Store persistence operations (load/save/delete) |
| `internal/queue/queue.go` | 187, 191, 240, 265, 298, 434, 456, 470, 474, 573, 577 | Queue persistence and track lifecycle |
| `internal/source/manager.go` | 69, 73, 76, 79, 91, 139, 158, 189, 318, 350, 354, 359, 391 | yt-dlp binary management |
| `internal/source/ytdlp.go` | 57 | Title fetch failure |

---

## Emoji-Laden Logging (Resolver + CLI Feedback)

These use emoji in log messages and Mumble chat feedback. Mixed severity:

| File | Line | Statement | Type |
|------|------|-----------|------|
| `internal/source/resolver.go` | 71 | `"⏳ Resolving %s with %T..."` | Logged via `c.logger` callback → Mumble chat + stderr |
| `internal/source/resolver.go` | 78 | `"❌ %s failed: %v"` | Same |
| `internal/source/resolver.go` | 85 | `"✅ Resolved: %s"` | Same |
| `internal/source/resolver.go` | 103 | `"🔄 Retry %d/%d for %s (%v wait)..."` | Same |
| `cmd/sawt/main.go` | 283 | `"❌ %v"` | CLI error response |
| `cmd/sawt/main.go` | 359 | `"🎵 Now playing: %s"` | Mumble bot response |
| `cmd/sawt/main.go` | 365–432 | `"🔊 Volume: %d%%"` / `"🔊 Unmuted"` | Mumble bot responses |

**Note:** The emoji in `resolver.go` lines 71, 78, 85, 103 are sent to Mumble chat via the `ResolveLogger` callback AND also logged to stderr. The Mumble chat ones (lines 78, 85, 103) are intentional UX. The `log.Printf` on 71, 79, 86, 104 are stderr-only debug.

---

## Recommendations

1. **Delete `cmd/debug-mumble/main.go` entirely** — it's not part of production.
2. **Remove or gate (via debug flag) the jitter buffer logs** in `internal/audio/jitter.go` — lines 80, 116, 123, 132, 147, 197 are per-packet and will flood logs.
3. **Consider adding a `debug` log level** to gate the verbose resolver and client logging, or reduce to `log.Infof` for startup/shutdown and keep only errors at `log.Errorf`.
4. The emoji in `resolver.go` Mumble chat feedback (lines 78, 85, 103) are fine as UX. The corresponding `log.Printf` calls (71, 79, 86, 104) to stderr should use a less verbose format or be gated.