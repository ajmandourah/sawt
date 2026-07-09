// Package main is the entry point for the Sawt Mumble music bot.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
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
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(os.Stderr)

	// Register API port flag BEFORE config.Load() (which calls flag.Parse())
	apiPort := flag.Int("api-port", 7071, "Port for the HTTP API server")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	log.Printf("Sawt (صوت) starting...")
	log.Printf("Server: %s | User: %s | Channel: %s", cfg.Server, cfg.Username, cfg.Channel)

	// Connect to Mumble
	client, err := mumble.New(cfg)
	if err != nil {
		log.Fatalf("Mumble connection failed: %v", err)
	}

	// Create audio engine
	engine := audio.New(client, cfg.Stereo)

	// Create queue manager
	qm := queue.New(engine)

	// Create source resolver chain (order matters):
	// 1. LocalResolver — files & directories
	// 2. YtDlpResolver — YouTube, SoundCloud, etc. (tried before DirectResolver)
	// 3. DirectResolver — plain HTTP streams (fallback if yt-dlp can't resolve)
	chain := source.NewChain(
		&source.LocalResolver{},
		source.NewYtDlpResolver(cfg.YtDlpPath),
		&source.DirectResolver{},
	)

	// Setup command dispatcher
	dispatcher := command.New(cfg.Prefix)
	setupCommands(dispatcher, client, qm, chain, cfg)

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
	apiStore := store.New(cfg.MusicDir, cfg.YtDlpPath) // reuse ytdlp path as ffprobe fallback

	// Create and start the HTTP API server
	apiSrv := api.New(api.Config{
		Port:     *apiPort,
		Store:    apiStore,
		QueueMgr: qm,
		Engine:   engine,
		MusicDir: cfg.MusicDir,
		ProbeCmd: cfg.YtDlpPath, // reuse; ideally pass ffprobe path separately
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := apiSrv.Start(ctx); err != nil {
			log.Printf("API server error: %v", err)
		}
	}()

	log.Printf("Sawt is online and listening for commands (prefix: %s)", cfg.Prefix)

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

func setupCommands(d *command.Dispatcher, client *mumble.Client, qm *queue.Manager, chain *source.Chain, cfg *config.Config) {
	// !help
	d.Register("help", func(user *gumble.User, action *command.Action) string {
		return fmt.Sprintf(`<b><font color="#7cfc00">Sawt (صوت)</font></b> — Mumble Music Bot<br><br>
<b>Commands:</b><br>
<b>%shelp</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Show this help message<br>
<b>%sping</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Check if bot is alive<br>
<b>%splay &lt;src&gt;</b>&nbsp;&nbsp;&nbsp;Play a file, URL, or directory<br>
<b>%sstop</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Stop playback and clear queue<br>
<b>%sskip</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Skip to next track<br>
<b>%spause</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Pause playback<br>
<b>%sresume</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Resume playback<br>
<b>%squeue</b>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Show current queue<br>
<b>%snowplaying</b>&nbsp;&nbsp;Show current track`,
			cfg.Prefix, cfg.Prefix, cfg.Prefix, cfg.Prefix,
			cfg.Prefix, cfg.Prefix, cfg.Prefix, cfg.Prefix, cfg.Prefix)
	})

	// !ping
	d.Register("ping", func(user *gumble.User, action *command.Action) string {
		return "Pong! 🏓"
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
			return handleDirResolve(ctx, user, input, chain, qm)
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
