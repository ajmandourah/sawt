package source

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// YtDlpResolver uses yt-dlp to extract direct media URLs from
// YouTube, SoundCloud, Bandcamp, and other supported sites.
type YtDlpResolver struct {
	binary string // path to yt-dlp binary
}

// NewYtDlpResolver creates a resolver with the given yt-dlp binary path.
// Use "" to search PATH.
func NewYtDlpResolver(binary string) *YtDlpResolver {
	if binary == "" {
		binary = "yt-dlp"
	}
	return &YtDlpResolver{binary: binary}
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
	// Check if yt-dlp is available.
	if _, err := exec.LookPath(r.binary); err != nil {
		return nil, fmt.Errorf("yt-dlp is not installed or not in PATH")
	}

	url := strings.TrimSpace(input)

	// Get the title first (fast, no download).
	title := url
	titleCmd := exec.CommandContext(ctx, r.binary, "--print", "title", "--no-playlist", "--restrict-filenames", url)
	if titleOut, err := titleCmd.Output(); err == nil {
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
	cmd := exec.Command(r.binary, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("yt-dlp check: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
