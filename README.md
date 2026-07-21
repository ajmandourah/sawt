# Sawt (صوت)

> A lightweight, self-hosted Mumble music bot written in Go.

[![Go Version](https://img.shields.io/badge/go-1.22+-00ADD8.svg)](https://golang.org/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Sawt is a music bot for mumble. it connects to your Mumble server, joins a channel, and streams music from local files, direct URLs, YouTube, SoundCloud, or any HTTP audio stream. you can Control it through chat commands in Mumble or the built-in web interface.

I made it because most popular music bots for mumble are not maintained anymore. additionally most music bots are written in python and need some serious performance boost. that's why I choose go as a backebd.

---

## Features

- **Multi-source playback** — local files, direct HTTP streams, YouTube, SoundCloud, Bandcamp, internet radio
- **Web UI** — library browser, queue manager, playlist creation, file upload, playback history
- **Chat commands** — `!play`, `!skip`, `!pause`, `!resume`, `!queue`, `!help` and more
- **Stereo audio** — optional stereo Opus encoding (Mumble 1.4.0+)
- **Jitter buffer** — configurable smoothing for network-heavy deployments
- **Persistence** — playlists, URLs, and play history saved to disk
- **yt-dlp auto-management** — binary auto-downloaded from GitHub, self-updates daily
- **FFmpeg pipeline** — streaming audio decode with no temporary files on disk (except yt-dlp)

---

## Requirements

| Dependency | Purpose | Required |
|------------|---------|----------|
| Go 1.22+ | Build the binary | Yes |
| ffmpeg | Audio decoding & format conversion | Yes |
| libopus-dev | Opus encoding (via CGO) | Yes |
| ffprobe | Duration probing for library metadata | Recommended |

> **Note:** yt-dlp is no longer a manual dependency. Sawt downloads it automatically from GitHub on first run and stores it in the data directory. It also self-updates daily.

**Ubuntu/Debian:**

```bash
sudo apt install ffmpeg libopus-dev
```

---

## Quick Start

### Build & Run

```bash
go build -o sawt ./cmd/sawt/
./sawt -server your-mumble-server:64738 -pass yourpassword -user "Sawt Bot" -channel "Music"
```

The binary embeds the web UI, so no separate static file serving is needed.
this is one of the great features as you literally only needs one executable file.

### Docker

**Pre-built image (recommended):**

```bash
docker pull ghcr.io/ajmandourah/sawt:latest
docker run -d --name sawt \
  -v /path/to/music:/music:ro \
  -v /path/to/data:/data \
  -e SERVER=your-server:64738 \
  -e USERNAME=SawtBot \
  -e PASSWORD=yourpassword \
  -e CHANNEL=Music \
  ghcr.io/ajmandourah/sawt:latest
```

**Build from source:**

```bash
docker build -t sawt .
docker run -d --name sawt \
  -v /path/to/music:/music:ro \
  -v /path/to/data:/data \
  -e SERVER=your-server:64738 \
  -e USERNAME=SawtBot \
  -e PASSWORD=yourpassword \
  -e CHANNEL=Music \
  sawt
```

See [Docker Configuration](#docker-configuration) for all environment variables.

### Docker Compose

**Using the pre-built GHCR image (recommended):**

```yaml
# docker-compose.yml
services:
  sawt:
    image: ghcr.io/ajmandourah/sawt:latest
    container_name: sawt
    restart: unless-stopped
    ports:
      - "7071:7071"
    volumes:
      - ./music:/music:ro
      - ./data:/data
    environment:
      - SERVER=your-server:64738
      - USERNAME=SawtBot
      - PASSWORD=yourpassword
      - CHANNEL=Music
```

**Build from source:**

```yaml
# docker-compose.yml
services:
  sawt:
    build: .
    container_name: sawt
    restart: unless-stopped
    ports:
      - "7071:7071"
    volumes:
      - ./music:/music:ro
      - ./data:/data
    environment:
      - SERVER=your-server:64738
      - USERNAME=SawtBot
      - PASSWORD=yourpassword
      - CHANNEL=Music
```

```bash
docker compose up -d
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER` | (none) | Mumble server address (host:port) |
| `USERNAME` | "Sawt Bot" | Bot username |
| `PASSWORD` | "" | Server password |
| `CHANNEL` | "Music" | Channel to join on connect |
| `PREFIX` | "!" | Command prefix in Mumble chat |
| `STEREO` | "false" | Enable stereo audio (requires Mumble 1.4.0+) |
| `JITTER` | "false" | Enable jitter buffer for smoother playback |
| `JITTER_DELAY` | "100" | Jitter buffer delay in milliseconds |
| `BUFFER` | "128" | Audio buffer size in frames |
| `WEBUI_PORT` | "7071" | Port for the web interface |
| `WEBUI_ADDR` | "0.0.0.0" | Bind address for web UI |

> **Note:** yt-dlp is auto-managed and downloaded from GitHub on first run. No `YTDLP` variable needed.

---

## Configuration

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-server` | (none) | Mumble server address (host:port) |
| `-pass` | "" | Server password |
| `-user` | "Sawt" | Bot username |
| `-channel` | "Music" | Channel to join on connect |
| `-cert` | "" | Path to TLS client certificate |
| `-key` | "" | Path to TLS client private key |
| `-music-dir` | "./music" | Directory to scan for local audio files |
| `-data-dir` | "./data" | Directory for persistence (playlists, history, URLs) |
| `-prefix` | "!" | Command prefix in Mumble chat |
| `-stereo` | false | Enable stereo audio (requires Mumble 1.4.0+) |
| `-jitter` | false | Enable jitter buffer for smoother playback |
| `-jitter-delay` | 100 | Jitter buffer delay in milliseconds |
| `-buffer` | 128 | Audio buffer size in frames |
| `-config` | "" | Path to YAML config file (flags override file values) |
| `-webui-port` | 7071 | Port for the web interface |
| `-webui-addr` | "0.0.0.0" | Bind address for web UI |

### YAML Config File

Create a `config.yaml` (or any path with `-config`):

```yaml
server: "127.0.0.1:64738"
username: "Sawt Bot"
password: "yourpassword"
channel: "Music"
music-dir: "./music"
data-dir: "./data"
prefix: "!"
stereo: false
jitter: false
jitter-delay: 100
buffer: 128
tls-cert: "/path/to/cert.pem"
tls-key: "/path/to/key.pem"
```

CLI flags always take precedence over file values.

---

## Chat Commands

All commands use the configured prefix (default `!`):

| Command | Description |
|---------|-------------|
| `!help` | Show available commands |
| `!ping` | Check if bot is alive |
| `!play <track>` | Play a file, URL, or directory |
| `!stop` | Stop playback and clear the queue |
| `!skip` | Skip to the next track |
| `!pause` | Pause current playback |
| `!resume` | Resume from where you paused |
| `!queue` | Show the current queue |
| `!nowplaying` | Show what is currently playing |

**`!play` accepts:**

- Local file paths (e.g. `!play /music/song.mp3`)
- Direct HTTP URLs (e.g. `!play https://example.com/stream.mp3`)
- YouTube/SoundCloud/Bandcamp URLs (resolved via yt-dlp)
- Directory paths (enqueues all audio files recursively)

---

## Web Interface

Access the web UI at `http://<server-ip>:7071`. Features:

- **Library** — browse all local tracks, search, add URLs, bulk-select for playlists
- **Queue** — now-playing card with progress bar, play/pause/skip controls, queue management
- **Playlists** — create, play, and delete playlists
- **History** — view recently played tracks and replay them
- **Upload** — drag-and-drop or browse to upload audio files (MP3, WAV, OGG, FLAC, M4A, etc.)

The web UI shares the same playback engine as chat commands.

---

## Audio Pipeline

```
Source → FFmpeg → PCM (s16le, 48kHz) → Opus Encode → Mumble
```

- FFmpeg decodes any format to raw 16-bit signed PCM at 48kHz
- Audio is streamed through pipes — no temporary files for local/HTTP sources
- Stereo mode outputs 3,840 bytes per 20ms frame; mono outputs 1,920
- yt-dlp sources: yt-dlp downloads to a temp file → FFmpeg reads it → temp file is cleaned up
- Jitter buffer (optional): smooths out network jitter with configurable delay

---

## Data Persistence

Files are stored in the data directory (default `./data`):

| File | Contents |
|------|----------|
| `playlists.json` | Saved playlists with track references |
| `urls.json` | Added URLs and stream tracks |
| `history.json` | Playback history (last 100 entries) |

Files are saved every minute and on graceful shutdown. Corrupt files are reset automatically.

### To do 
a real database should be implemented instead of using json files.

---

## Architecture

```
cmd/sawt/          — Entry point, wires everything together
internal/config/   — CLI flags + YAML config loading
internal/mumble/   — gumble connection, auth, TLS, text dispatch
internal/audio/    — FFmpeg pipeline, PCM framing, Opus encoding, jitter buffer
internal/queue/    — Playback queue, state machine, history
internal/source/   — Source resolution chain (local, direct, yt-dlp)
internal/command/  — Chat command parser and dispatcher
internal/api/      — HTTP REST API + embedded web UI
```

The full architectural blueprint is in [ARCHITECTURE.md](ARCHITECTURE.md).

---


## License

MIT
