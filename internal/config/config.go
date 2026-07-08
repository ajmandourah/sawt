// Package config loads and validates bot configuration from CLI flags
// and an optional YAML file.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds all runtime configuration for the Sawt bot.
type Config struct {
	// Mumble server connection
	Server   string // host:port (e.g. "127.0.0.1:64738")
	Username string
	Password string
	Channel  string // channel to join on connect

	// TLS
	TLSCert string // path to client certificate (optional)
	TLSKey  string // path to client private key (optional)

	// Audio
	MusicDir string // directory to scan for local files

	// Commands
	Prefix string // command prefix (default: "!")

	// yt-dlp
	YtDlpPath string // path to yt-dlp binary (default: "yt-dlp")

	// Audio
	Stereo bool // enable stereo audio (requires Mumble 1.4.0+)
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Server:    "127.0.0.1:64738",
		Username:  "Sawt",
		Channel:   "Music",
		Prefix:    "!",
		YtDlpPath: "yt-dlp",
		MusicDir:  "./music",
		Stereo:    false,
	}
}

// Load parses CLI flags and an optional YAML config file.
// Flags take precedence over file values.
func Load() (*Config, error) {
	cfg := DefaultConfig()

	// CLI flags
	flagServer := flag.String("server", cfg.Server, "Mumble server address (host:port)")
	flagUser := flag.String("user", cfg.Username, "Bot username")
	flagPass := flag.String("pass", "", "Server password")
	flagCh := flag.String("channel", cfg.Channel, "Channel to join")
	flagCert := flag.String("cert", "", "Path to TLS certificate")
	flagKey := flag.String("key", "", "Path to TLS private key")
	flagMusic := flag.String("music-dir", cfg.MusicDir, "Directory with local music files")
	flagPrefix := flag.String("prefix", cfg.Prefix, "Command prefix character")
	flagYtDlp := flag.String("ytdlp", cfg.YtDlpPath, "Path to yt-dlp binary")
	flagStereo := flag.Bool("stereo", cfg.Stereo, "Enable stereo audio")
	flagConfig := flag.String("config", "", "Path to YAML config file")
	flag.Parse()

	// Load YAML config if specified
	if *flagConfig != "" {
		if err := loadYAML(cfg, *flagConfig); err != nil {
			return nil, fmt.Errorf("load config file: %w", err)
		}
	}

	// Apply CLI flags (they override file values)
	cfg.Server = *flagServer
	cfg.Username = *flagUser
	if *flagPass != "" {
		cfg.Password = *flagPass
	} else {
		cfg.Password = cfg.Password // keep from YAML if set
	}
	cfg.Channel = *flagCh
	cfg.TLSCert = *flagCert
	cfg.TLSKey = *flagKey
	cfg.MusicDir = *flagMusic
	cfg.Prefix = *flagPrefix
	cfg.YtDlpPath = *flagYtDlp
	cfg.Stereo = *flagStereo

	// Validate
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func loadYAML(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Minimal YAML parsing without external deps for now.
	// We'll add yaml.v3 later if needed.
	// For now, parse key=value lines.
	for _, line := range splitLines(string(data)) {
		line = trim(line)
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := cutKeyValue(line)
		if !ok {
			continue
		}
		switch k {
		case "server":
			cfg.Server = v
		case "username":
			cfg.Username = v
		case "password":
			cfg.Password = v
		case "channel":
			cfg.Channel = v
		case "tls-cert":
			cfg.TLSCert = v
		case "tls-key":
			cfg.TLSKey = v
		case "music-dir":
			cfg.MusicDir = v
		case "prefix":
			cfg.Prefix = v
		case "ytdlp":
			cfg.YtDlpPath = v
		case "stereo":
			cfg.Stereo = v == "true"
		}
	}

	return nil
}

func (c *Config) validate() error {
	if c.Server == "" {
		return fmt.Errorf("server address is required")
	}
	if c.Username == "" {
		return fmt.Errorf("username is required")
	}
	if c.TLSCert != "" && c.TLSKey == "" {
		return fmt.Errorf("tls-key must be provided when tls-cert is set")
	}
	if c.TLSKey != "" && c.TLSCert == "" {
		return fmt.Errorf("tls-cert must be provided when tls-key is set")
	}
	return nil
}

// Simple helpers to avoid importing strings/bytes excessively.
func splitLines(s string) []string {
	var lines []string
	var cur []byte
	for _, b := range []byte(s) {
		if b == '\n' {
			lines = append(lines, string(cur))
			cur = cur[:0]
		} else {
			cur = append(cur, b)
		}
	}
	if len(cur) > 0 {
		lines = append(lines, string(cur))
	}
	return lines
}

func trim(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

func cutKeyValue(s string) (key, value string, ok bool) {
	for i, b := range s {
		if b == '=' {
			key = trim(s[:i])
			value = trim(s[i+1:])
			return key, value, true
		}
	}
	return "", "", false
}

// IsLocalFile returns true if the path exists and is a regular file.
func IsLocalFile(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	info, err := os.Stat(abs)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// IsDirectory returns true if the path exists and is a directory.
func IsDirectory(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	info, err := os.Stat(abs)
	if err != nil {
		return false
	}
	return info.IsDir()
}
