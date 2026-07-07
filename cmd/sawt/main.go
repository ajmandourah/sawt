// Package main is the entry point for the Sawt Mumble music bot.
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"layeh.com/gumble/gumble"

	"github.com/ladis/sawt/internal/audio"
	"github.com/ladis/sawt/internal/command"
	"github.com/ladis/sawt/internal/config"
	"github.com/ladis/sawt/internal/mumble"
	"github.com/ladis/sawt/internal/queue"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(os.Stderr)

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
	engine := audio.New(client)

	// Create queue manager
	qm := queue.New(engine)

	// Setup command dispatcher
	dispatcher := command.New(cfg.Prefix)
	setupCommands(dispatcher, client, qm, cfg)

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

	log.Printf("Sawt is online and listening for commands (prefix: %s)", cfg.Prefix)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("Shutting down...")
	qm.Stop()
	client.Stop()
	os.Exit(0)
}

func setupCommands(d *command.Dispatcher, client *mumble.Client, qm *queue.Manager, cfg *config.Config) {
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

		source := strings.TrimSpace(action.Args)

		// Handle directory
		if config.IsDirectory(source) {
			return handleDirectory(user, source, qm, cfg)
		}

		// Handle URL (direct HTTP stream) — check before local file
		if isURL(source) {
			track := &audio.Track{
				Title:       source,
				Source:      source,
				SourceType:  audio.SourceDirect,
				RequestedBy: user.Name,
			}
			qm.Enqueue(track)
			return fmt.Sprintf("▶ Playing: %s", source)
		}

		// Handle local file — validate it exists and is readable
		absPath, err := filepath.Abs(source)
		if err != nil {
			return fmt.Sprintf("❌ Invalid path: %s", source)
		}
		info, err := os.Stat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Sprintf("❌ File not found: %s", source)
			}
			return fmt.Sprintf("❌ Cannot access file: %s (%v)", source, err)
		}
		if info.IsDir() {
			return fmt.Sprintf("❌ %s is a directory. Use it without trailing slash or specify files.", source)
		}

		track := &audio.Track{
			Title:       filepath.Base(source),
			Source:      absPath,
			SourceType:  audio.SourceLocal,
			RequestedBy: user.Name,
		}
		qm.Enqueue(track)
		return fmt.Sprintf("▶ Playing: %s", track.Title)
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

// isURL returns true if the source looks like a URL (http/https). This check
// runs before local file checks so paths like "http://..." are not treated
// as nonexistent local files.
func isURL(source string) bool {
	return strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")
}

// handleDirectory scans a directory for audio files and enqueues them.
func handleDirectory(user *gumble.User, dirPath string, qm *queue.Manager, cfg *config.Config) string {
	// Known audio extensions
	audioExts := map[string]bool{
		".mp3": true, ".wav": true, ".flac": true, ".ogg": true,
		".m4a": true, ".wma": true, ".aac": true, ".opus": true,
	}

	var files []string
	filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if audioExts[ext] {
			files = append(files, path)
		}
		return nil
	})

	if len(files) == 0 {
		return "No audio files found in directory"
	}

	// Sort for consistent ordering
	sort.Strings(files)

	// Enqueue all files
	for _, f := range files {
		track := &audio.Track{
			Title:       filepath.Base(f),
			Source:      f,
			SourceType:  audio.SourceLocal,
			RequestedBy: user.Name,
		}
		qm.Enqueue(track)
	}

	return fmt.Sprintf("📁 Enqueued %d files from %s", len(files), filepath.Base(dirPath))
}
