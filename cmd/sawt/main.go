// Package main is the entry point for the Sawt Mumble music bot.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"layeh.com/gumble/gumble"

	"github.com/ladis/sawt/internal/api"
	"github.com/ladis/sawt/internal/api/store"
	"github.com/ladis/sawt/internal/audio"
	"github.com/ladis/sawt/internal/command"
	"github.com/ladis/sawt/internal/config"
	"github.com/ladis/sawt/internal/mumble"
	"github.com/ladis/sawt/internal/queue"
	"github.com/ladis/sawt/internal/source"
	"strconv"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(os.Stderr)

	version := gitVersion()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	log.Printf("Sawt (صوت) starting...")
	log.Printf("Server: %s | User: %s | Channel: %s", cfg.Server, cfg.Username, cfg.Channel)

	// Ensure data directory exists
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// Create context for cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect to Mumble
	client, err := mumble.New(cfg)
	if err != nil {
		log.Fatalf("Mumble connection failed: %v", err)
	}

	// Create audio engine
	engine := audio.New(client, cfg.Stereo, cfg.JitterBuf, cfg.JitterDelay, cfg.BufferFrames)

	// Create queue manager
	qm := queue.New(engine)
	qm.LoadHistory(filepath.Join(cfg.DataDir, "history.json"))

	// Attach shared volume controller
	qm.SetVolumeController(engine.VolumeController())
	qm.LoadVolume(filepath.Join(cfg.DataDir, "volume.json"))

	// Ensure yt-dlp binary is available (downloads from GitHub if missing).
	ytDlpManager := source.NewManager(cfg.DataDir)
	if err := ytDlpManager.EnsureAvailable(); err != nil {
		log.Printf("WARNING: yt-dlp not available: %v — YouTube/SoundCloud playback disabled", err)
	} else {
		log.Printf("yt-dlp version: %s", ytDlpManager.Version())
		// Start daily update check (first check after 1 hour, then every 24h).
		ytDlpManager.RunDailyUpdate(ctx)
	}

	// Create source resolver chain (order matters):
	// 1. LocalResolver — files & directories
	// 2. YtDlpResolver — YouTube, SoundCloud, etc. (tried before DirectResolver)
	// 3. DirectResolver — plain HTTP streams (fallback if yt-dlp can't resolve)
	chain := source.NewChain(
		&source.LocalResolver{},
		source.NewYtDlpResolver(ytDlpManager),
		&source.DirectResolver{},
	)

	// Setup command dispatcher
	dispatcher := command.New(cfg.Prefix)
	setupCommands(dispatcher, client, qm, chain, cfg, version)

	// Register text handler
	client.SetTextHandler(func(user *gumble.User, message string) {
		action := dispatcher.Parse(message)
		if action == nil {
			return // not a command
		}
		response := dispatcher.Dispatch(user, action)
		if response != "" {
			client.ReplyToUser(user, response)
		}
	})

	// ---- API Server ----
	// Create the API store (scans music dir for tracks)
	apiStore := store.New(cfg.MusicDir, "ffprobe", cfg.DataDir)
	// Load persisted data
	apiStore.LoadPlaylists(filepath.Join(cfg.DataDir, "playlists.json"))
	apiStore.LoadURLs(filepath.Join(cfg.DataDir, "urls.json"))

	// Create and start the WebUI server
	webuiSrv := api.New(api.Config{
		Port:        cfg.WebUIPort,
		Addr:        cfg.WebUIAddr,
		Version:     version,
		Store:       apiStore,
		QueueMgr:    qm,
		Engine:      engine,
		MusicDir:    cfg.MusicDir,
		ProbeCmd:    "ffprobe",
		SourceChain: chain,
	})

	go func() {
		if err := webuiSrv.Start(ctx); err != nil {
			log.Printf("WebUI server error: %v", err)
		}
	}()

	log.Printf("Sawt v%s is online and listening for commands (prefix: %s)", version, cfg.Prefix)

	// --- Join the target channel as the VERY LAST step ---
	// All initialization is complete, the readRoutine has drained its buffer,
	// and Self.Session/channel tree are fully populated.
	client.JoinChannel(cfg.Channel)

	// Set the bot's comment (hover text) to the help message.
	webuiLink := ""
	if cfg.WebUIURL != "" {
		webuiLink = `<br><br><a href="` + cfg.WebUIURL + `">Web Interface</a>`
	}
	client.Self().Comment = `<b><font color="#7cfc00">Sawt (صوت) v` + version + `</font></b> — Mumble Music Bot<br><br>
<b>Commands:</b><br>
<b>` + cfg.Prefix + `help</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Show this help message<br>
<b>` + cfg.Prefix + `ping</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Check if bot is alive<br>
<b>` + cfg.Prefix + `play &lt;src&gt;</b>&nbsp;&nbsp;&nbsp;Play a file, URL, or directory<br>
<b>` + cfg.Prefix + `stop</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Stop playback and clear queue<br>
<b>` + cfg.Prefix + `skip</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Skip to next track<br>
<b>` + cfg.Prefix + `pause</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Pause playback<br>
<b>` + cfg.Prefix + `resume</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Resume playback<br>
<b>` + cfg.Prefix + `queue</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Show current queue<br>
<b>` + cfg.Prefix + `nowplaying</b>&nbsp;&nbsp;Show current track<br>
<b>` + cfg.Prefix + `volume</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Set volume 0–200 (default: 50)<br>
<b>` + cfg.Prefix + `mute</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Mute audio<br>
<b>` + cfg.Prefix + `unmute</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Unmute audio<br>
<b>` + cfg.Prefix + `vol+</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Increase volume by 10<br>
<b>` + cfg.Prefix + `vol-</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Decrease volume by 10<br>
<br>
<a href="https://github.com/ajmandourah/sawt">GitHub Repo</a> · <a href="https://github.com/ajmandourah/sawt/issues">Report Issues</a>` + webuiLink

	// Send welcome message to the channel.
	webuiWelcome := ""
	if cfg.WebUIURL != "" {
		webuiWelcome = `<br><a href="` + cfg.WebUIURL + `">Web Interface</a>`
	}
	welcomeMsg := fmt.Sprintf(
		`<b><font color="#7cfc00">Sawt (صوت) v%s</font></b> — Mumble Music Bot<br><br>
I'm online and ready to play music! Use <b>%splay &lt;track&gt;</b> to start.<br><br>
<a href="https://github.com/ajmandourah/sawt">GitHub Repo</a> · <a href="https://github.com/ajmandourah/sawt/issues">Report Issues</a>`+webuiWelcome,
		version, cfg.Prefix)
	client.SendMessage(welcomeMsg)

	// Periodically save URLs and volume
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// Final save on shutdown
				apiStore.SaveURLs(filepath.Join(cfg.DataDir, "urls.json"))
				apiStore.SavePlaylists(filepath.Join(cfg.DataDir, "playlists.json"))
				qm.SaveHistory(filepath.Join(cfg.DataDir, "history.json"))
				qm.SaveVolume(filepath.Join(cfg.DataDir, "volume.json"))
				return
			case <-ticker.C:
				apiStore.SaveURLs(filepath.Join(cfg.DataDir, "urls.json"))
				apiStore.SavePlaylists(filepath.Join(cfg.DataDir, "playlists.json"))
				qm.SaveHistory(filepath.Join(cfg.DataDir, "history.json"))
				qm.SaveVolume(filepath.Join(cfg.DataDir, "volume.json"))
			}
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("Shutting down...")
	cancel() // stop API server
	qm.Stop()
	client.Stop()
	os.Exit(0)
}

// gitVersion reads the version from git describe --tags.
// Falls back to "dev" if git is unavailable or not a repo.
func gitVersion() string {
	cmd := exec.Command("git", "describe", "--tags", "--always", "--dirty", "--abbrev=7")
	out, err := cmd.Output()
	if err != nil {
		return "dev"
	}
	return strings.TrimSpace(string(out))
}

func setupCommands(d *command.Dispatcher, client *mumble.Client, qm *queue.Manager, chain *source.Chain, cfg *config.Config, version string) {
	// !help
	d.Register("help", func(user *gumble.User, action *command.Action) string {
		p := cfg.Prefix
		webuiLink := ""
		if cfg.WebUIURL != "" {
			webuiLink = `<br><br><a href="` + cfg.WebUIURL + `">Web Interface</a>`
		}
		return `<b><font color="#7cfc00">Sawt (صوت) v` + version + `</font></b> — Mumble Music Bot<br><br>
<b>Commands:</b><br>
<b>` + p + `help</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Show this help message<br>
<b>` + p + `ping</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Check if bot is alive<br>
<b>` + p + `play &lt;src&gt;</b>&nbsp;&nbsp;&nbsp;Play a file, URL, or directory<br>
<b>` + p + `stop</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Stop playback and clear queue<br>
<b>` + p + `skip</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Skip to next track<br>
<b>` + p + `pause</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Pause playback<br>
<b>` + p + `resume</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Resume playback<br>
<b>` + p + `queue</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Show current queue<br>
<b>` + p + `nowplaying</b>&nbsp;&nbsp;Show current track<br>
<b>` + p + `volume</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Set volume 0–200 (default: 50)<br>
<b>` + p + `mute</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Mute audio<br>
<b>` + p + `unmute</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Unmute audio<br>
<b>` + p + `vol+</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Increase volume by 10<br>
<b>` + p + `vol-</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Decrease volume by 10<br>
<br>
<a href="https://github.com/ajmandourah/sawt">GitHub Repo</a> · <a href="https://github.com/ajmandourah/sawt/issues">Report Issues</a>` + webuiLink
	})

	// !play
	d.Register("play", func(user *gumble.User, action *command.Action) string {
		if action.Args == "" {
			return fmt.Sprintf("Usage: %splay <file|url|directory>", cfg.Prefix)
		}

		input := strings.TrimSpace(action.Args)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// Try directory first
		if config.IsDirectory(input) {
			result := handleDirResolve(ctx, user, input, chain, qm)
			// Auto-play if idle after enqueuing directory
			if qm.State() == queue.StateIdle {
				qm.PlayQueue()
			}
			return result
		}

		// Resolve through the chain
		resolved, err := chain.Resolve(ctx, input)
		if err != nil {
			return fmt.Sprintf("❌ %v", err)
		}

		track := &audio.Track{
			Title:       resolved.Title,
			Source:      resolved.URL,
			SourceType:  resolved.Type,
			RequestedBy: user.Name,
		}
		qm.Enqueue(track)

		// Auto-play if idle (nothing currently playing)
		if qm.State() == queue.StateIdle {
			qm.PlayQueue()
		}

		return fmt.Sprintf("▶ Playing: %s", resolved.Title)
	})

	// !stop
	d.Register("stop", func(user *gumble.User, action *command.Action) string {
		qm.Stop()
		return "⏹ Stopped"
	})

	// !skip
	d.Register("skip", func(user *gumble.User, action *command.Action) string {
		if qm.State() == queue.StateIdle {
			return "Nothing playing"
		}
		qm.Skip()
		return "⏭ Skipping..."
	})

	// !pause
	d.Register("pause", func(user *gumble.User, action *command.Action) string {
		if qm.State() != queue.StatePlaying {
			return "Nothing to pause"
		}
		qm.Pause()
		return "⏸ Paused"
	})

	// !resume
	d.Register("resume", func(user *gumble.User, action *command.Action) string {
		if qm.State() != queue.StatePaused {
			return "Nothing to resume"
		}
		qm.Resume()
		return "▶ Resumed"
	})

	// !queue
	d.Register("queue", func(user *gumble.User, action *command.Action) string {
		items := qm.Queue()
		if len(items) == 0 {
			curr := qm.Current()
			if curr != nil {
				return fmt.Sprintf("Now playing: %s (queue empty)", curr.Title)
			}
			return "Queue is empty"
		}

		lines := []string{"📋 Queue:"}
		for i, t := range items {
			lines = append(lines, fmt.Sprintf("  %d. %s", i+1, t.Title))
		}
		return strings.Join(lines, "\n")
	})

	// !nowplaying
	d.Register("nowplaying", func(user *gumble.User, action *command.Action) string {
		curr := qm.Current()
		if curr == nil {
			return "Nothing playing right now"
		}
		return fmt.Sprintf("🎵 Now playing: %s (requested by %s)", curr.Title, curr.RequestedBy)
	})

	// !volume <0-200>
	d.Register("volume", func(user *gumble.User, action *command.Action) string {
		if action.Args == "" {
			return fmt.Sprintf("🔊 Volume: %d%%", qm.Volume())
		}
		pct, err := strconv.Atoi(action.Args)
		if err != nil || pct < 0 || pct > 200 {
			return "Usage: " + cfg.Prefix + "volume <0-200>"
		}
		actual := qm.SetVolume(pct)
		return fmt.Sprintf("🔊 Volume: %d%%", actual)
	})

	// !vol (alias for !volume)
	d.Register("vol", func(user *gumble.User, action *command.Action) string {
		if action.Args == "" {
			return fmt.Sprintf("🔊 Volume: %d%%", qm.Volume())
		}
		pct, err := strconv.Atoi(action.Args)
		if err != nil || pct < 0 || pct > 200 {
			return "Usage: " + cfg.Prefix + "vol <0-200>"
		}
		actual := qm.SetVolume(pct)
		return fmt.Sprintf("🔊 Volume: %d%%", actual)
	})

	// !vol+ (increment)
	d.Register("vol+", func(user *gumble.User, action *command.Action) string {
		step := 10
		if action.Args != "" {
			if n, err := strconv.Atoi(action.Args); err == nil && n > 0 {
				step = n
			}
		}
		current := qm.Volume()
		actual := qm.SetVolume(current + step)
		return fmt.Sprintf("🔊 Volume: %d%%", actual)
	})

	// !vol- (decrement)
	d.Register("vol-", func(user *gumble.User, action *command.Action) string {
		step := 10
		if action.Args != "" {
			if n, err := strconv.Atoi(action.Args); err == nil && n > 0 {
				step = n
			}
		}
		current := qm.Volume()
		actual := qm.SetVolume(current - step)
		return fmt.Sprintf("🔊 Volume: %d%%", actual)
	})

	// !mute
	d.Register("mute", func(user *gumble.User, action *command.Action) string {
		qm.Mute()
		return "🔇 Muted"
	})

	// !unmute
	d.Register("unmute", func(user *gumble.User, action *command.Action) string {
		qm.Unmute()
		return "🔊 Unmuted"
	})
}

// handleDirResolve resolves a directory through the LocalResolver and enqueues all files.
func handleDirResolve(ctx context.Context, user *gumble.User, dirPath string, chain *source.Chain, qm *queue.Manager) string {
	localRes := &source.LocalResolver{}
	sources, err := localRes.ResolveDir(ctx, dirPath)
	if err != nil {
		return fmt.Sprintf("❌ %v", err)
	}

	for _, src := range sources {
		track := &audio.Track{
			Title:       src.Title,
			Source:      src.URL,
			SourceType:  src.Type,
			RequestedBy: user.Name,
		}
		qm.Enqueue(track)
	}

	return fmt.Sprintf("📁 Enqueued %d files", len(sources))
}
