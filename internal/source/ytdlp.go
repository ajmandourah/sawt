package source

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// YtDlpResolver uses yt-dlp to extract direct media URLs from
// YouTube, SoundCloud, Bandcamp, and other supported sites.
type YtDlpResolver struct {
	manager *Manager // manages the yt-dlp binary lifecycle
}

// NewYtDlpResolver creates a resolver with the given binary manager.
// The manager handles locating or downloading the yt-dlp binary.
func NewYtDlpResolver(manager *Manager) *YtDlpResolver {
	return &YtDlpResolver{manager: manager}
}

func (r *YtDlpResolver) CanHandle(input string) bool {
	if !isURL(input) {
		return false
	}
	// Only claim known streaming sites; let DirectResolver handle raw URLs.
	lower := strings.ToLower(input)
	knownSites := []string{
		"youtu", "soundcloud", "bandcamp", "spotify",
		"vimeo", "dailymotion", "twitch", "facebook",
		"instagram", "tiktok", "reddit", "9gag",
	}
	for _, site := range knownSites {
		if strings.Contains(lower, site) {
			return true
		}
	}
	return false
}

func (r *YtDlpResolver) Resolve(ctx context.Context, input string) (*ResolvedSource, error) {
	binary := r.manager.BinaryPath()
	if binary == "" {
		return nil, fmt.Errorf("yt-dlp binary path not available")
	}

	url := strings.TrimSpace(input)

	// Get the title first (fast, no download).
	title := url
	titleCmd := exec.CommandContext(ctx, binary, "--print", "title",
		"--no-playlist", "--restrict-filenames", "--no-warnings", url)
	titleStderr := new(strings.Builder)
	titleCmd.Stderr = titleStderr
	if titleOut, err := titleCmd.Output(); err != nil {
		log.Printf("yt-dlp title fetch failed for %s: %v (%s)", url, err, titleStderr.String())
	} else {
		if t := strings.TrimSpace(string(titleOut)); t != "" {
			title = t
		}
	}

	// Return the ORIGINAL URL — the engine will pipe yt-dlp → FFmpeg.
	// This lets yt-dlp handle the download with proper headers, avoiding
	// 403 errors from DASH URLs and double-processing of resolved URLs.
	return &ResolvedSource{
		URL:   url,
		Title: title,
		Type:  SourceYtDlp,
	}, nil
}

// CheckBinary verifies that yt-dlp is available and returns the version.
func (r *YtDlpResolver) CheckBinary() (string, error) {
	binary := r.manager.BinaryPath()
	if binary == "" {
		return "", fmt.Errorf("yt-dlp binary path not available")
	}
	cmd := exec.Command(binary, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("yt-dlp check: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
