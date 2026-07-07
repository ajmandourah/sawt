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
	// Fallback for any URL that other resolvers couldn't handle.
	// We conservatively say yes to any URL — the actual resolve will fail
	// if yt-dlp doesn't support the site.
	return isURL(input)
}

func (r *YtDlpResolver) Resolve(ctx context.Context, input string) (*ResolvedSource, error) {
	// Check if yt-dlp is available.
	if _, err := exec.LookPath(r.binary); err != nil {
		return nil, fmt.Errorf("yt-dlp is not installed or not in PATH")
	}

	// Get the title first (fast, no download).
	title := input
	titleCmd := exec.CommandContext(ctx, r.binary, "--print", "title", "--no-playlist", "--restrict-filenames", input)
	if titleOut, err := titleCmd.Output(); err == nil {
		if t := strings.TrimSpace(string(titleOut)); t != "" {
			title = t
		}
	}

	// yt-dlp -g to get direct URL, preferring audio-only formats.
	// -f ba: best audio-only format (m4a, webm, etc.)
	// This avoids DASH video URLs that FFmpeg can't decode to audio.
	args := []string{"-g", "--no-playlist", "--restrict-filenames", "-f", "ba", input}

	cmd := exec.CommandContext(ctx, r.binary, args...)
	out, err := cmd.Output()
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "NotFound") || strings.Contains(errMsg, "executable file not found") {
			return nil, fmt.Errorf("yt-dlp is not installed")
		}
		return nil, fmt.Errorf("yt-dlp failed for %s: %v", input, errMsg)
	}

	urls := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(urls) == 0 || urls[0] == "" {
		return nil, fmt.Errorf("yt-dlp returned no URL for %s", input)
	}

	directURL := strings.TrimSpace(urls[0])

	return &ResolvedSource{
		URL:   directURL,
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
