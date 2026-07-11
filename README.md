# Sawt (صوت)

Mumble music bot written in Go. The name means "sound" in Arabic.

I made this because I was tired of the existing music bots being either too heavy (Java, Python with tons of dependencies) or not self-hosted friendly. This is one single binary that you can run on your own server and control everything.

It connect to your Mumble server, join a channel, and play music from local files, YouTube, SoundCloud, or any direct audio URL. You control it through chat commands in Mumble or through the built-in web interface.

## Requirements

- Go 1.22 or higher
- ffmpeg (system package, used for audio decoding)
- ffprobe (usually comes with ffmpeg)
- yt-dlp (optional, only if you want YouTube/SoundCloud support)
- libopus development headers (for Opus encoding)

On Ubuntu/Debian:

```
sudo apt install ffmpeg yt-dlp libopus-dev
```

## Build and Run

```
go build -o sawt ./cmd/sawt/
./sawt -server your-mumble-server:64738 -pass yourpassword -user "Bot Name" -channel "Music" -music-dir ./music
```

The binary is about 12MB and it include the web UI embedded inside, so you dont need to serve any static files separately.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| -server | (required) | Mumble server address with port |
| -pass | (none) | Server password if required |
| -user | "Sawt Bot" | Bot username |
| -channel | "Music" | Channel to join on connect |
| -music-dir | (none) | Directory containing your music files |
| -prefix | "!" | Command prefix in chat |
| -stereo | false | Enable stereo audio output |
| -webui-port | 7071 | Port for the web interface |
| -webui-addr | 0.0.0.0 | Bind address for web UI |
| -data-dir | ./data | Where playlists and history are saved |
| -ytdlp | yt-dlp | Path to yt-dlp binary |
| -jitter-buf | 0 | Jitter buffer delay in milliseconds |
| -jitter-delay | 0 | Additional jitter delay |
| -buffer-frames | 100 | Audio buffer size in frames |

## Commands

All commands use the prefix (default is `!`):

- `!play <track>` - Play a track from your music directory or a URL
- `!stop` - Stop playback and clear the queue
- `!skip` - Skip to the next track in queue
- `!pause` - Pause current playback
- `!resume` - Resume from where you paused
- `!queue` - Show the current queue
- `!nowplaying` - Show what is currently playing
- `!help` - Show available commands

When you use `!play` with a URL, it will check if it is a YouTube or SoundCloud link and use yt-dlp to extract the audio. If it is a direct link to an audio file, it will play it directly. For local files, it search in the music directory you specified.

## Web Interface

The bot come with a built-in web UI that you can access on port 7071 (or whatever you set with -webui-port). It have the following features:

- Library tab - see all your tracks, search, add URLs, and select multiple tracks
- Queue tab - see what is playing, control playback, manage the queue
- Playlists tab - create and manage playlists from your library
- History tab - see playback history and replay old tracks
- Upload tab - upload audio files directly to your music directory

The web UI use the same playback engine as the chat commands, so the behavior is consistent. When you press play from the web, it go through the same source resolution chain.

## Audio Pipeline

The audio flow is pretty simple: source -> ffmpeg -> PCM bytes -> Opus encode -> Mumble.

For local files and direct URLs, ffmpeg read the file directly and output raw PCM. For YouTube and SoundCloud, yt-dlp first extract the direct audio URL and then ffmpeg process it. Everything is streamed through pipes, so there is no temporary video files being downloaded to disk (except for yt-dlp which download audio to a temp file and delete it after).

The PCM output from ffmpeg is read at 50Hz (20ms frames) which match the Mumble audio frame rate. This prevent buffer overflow and keep the playback smooth.

## Configuration File

Instead of using flags every time, you can create a config.yaml file:

```yaml
server: "your-server:64738"
username: "Sawt Bot"
password: "yourpassword"
channel: "Music"
musicDir: "./music"
prefix: "!"
stereo: false
webuiPort: 7071
ytdlpPath: "yt-dlp"
```

Then just run `./sawt` and it will load the config automatically. Command line flags still override the config file values.

## Data Persistence

Playlists, added URLs, and play history are saved in the data directory (default is `./data`). The files are saved automatically every minute and also when you stop the bot. If the JSON files get corrupted somehow, the bot will reset them instead of crashing.

## Notes

The bot is designed to be lightweight and run on small servers. I tested it on a VPS with 512MB RAM and it work fine. The memory usage is pretty stable because we stream everything and dont load full files into memory.

If you have any issue with audio quality, try adjusting the jitter buffer settings. The default is disabled which give the lowest latency but might have some stutter on slow connections.

For the stereo mode, you need to make sure your Mumble server support it and enable it with the -stereo flag. The bot will encode in stereo which use more bandwidth but sound better for music.

## License

MIT
