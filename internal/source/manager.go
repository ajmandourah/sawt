// Package source provides the yt-dlp binary manager.
// It locates the yt-dlp binary in the data directory, downloads it from
// GitHub if missing, and runs a daily self-update via `yt-dlp -U`.
package source

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// ytDlpGitHubURL is the base URL for yt-dlp GitHub releases.
	ytDlpGitHubURL = "https://github.com/yt-dlp/yt-dlp/releases"

	// ytDlpBinaryName is the filename of the managed binary.
	ytDlpBinaryName = "yt-dlp"

	// updateCheckInterval is how often the bot checks for updates.
	updateCheckInterval = 24 * time.Hour
)

// Manager manages the yt-dlp binary lifecycle.
type Manager struct {
	mu        sync.Mutex
	dataDir   string
	binary    string // absolute path to the yt-dlp binary
	version   string // cached version string
	lastCheck time.Time
}

// NewManager creates a Manager that stores the yt-dlp binary in dataDir.
func NewManager(dataDir string) *Manager {
	return &Manager{
		dataDir: dataDir,
		binary:  filepath.Join(dataDir, ytDlpBinaryName),
	}
}

// BinaryPath returns the absolute path to the yt-dlp binary.
func (m *Manager) BinaryPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.binary
}

// EnsureAvailable checks for the yt-dlp binary in the data directory.
// If it is missing or not executable, it downloads the latest release from GitHub.
func (m *Manager) EnsureAvailable() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if binary exists and is executable.
	if info, err := os.Stat(m.binary); err == nil && info.Mode()&0111 != 0 {
		log.Printf("yt-dlp: using existing binary at %s", m.binary)
		// Verify it actually works.
		if out, err := exec.Command(m.binary, "--version").Output(); err == nil {
			m.version = strings.TrimSpace(string(out))
			log.Printf("yt-dlp: version %s", m.version)
			return nil
		}
		log.Printf("yt-dlp: existing binary failed version check, re-downloading")
	}

	log.Printf("yt-dlp: binary not found or invalid, downloading from GitHub...")
	return m.downloadBinary()
}

// downloadBinary fetches the latest yt-dlp release from GitHub and extracts it.
func (m *Manager) downloadBinary() error {
	// Determine the correct asset based on architecture.
	assetURL := m.buildAssetURL()
	if assetURL == "" {
		return fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	log.Printf("yt-dlp: downloading from %s", assetURL)

	resp, err := http.Get(assetURL) //nolint:gosec // yt-dlp GitHub releases are trusted
	if err != nil {
		return fmt.Errorf("download yt-dlp: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download yt-dlp: HTTP %d", resp.StatusCode)
	}

	// Ensure data directory exists.
	if err := os.MkdirAll(m.dataDir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// Read the full response to detect the archive type.
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	// Detect archive type and extract the binary.
	src, tmpDir, err := extractFromData(data, m.dataDir)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	// Move to final location.
	if err := os.Rename(src, m.binary); err != nil {
		return fmt.Errorf("move binary: %w", err)
	}

	// Make executable.
	if err := os.Chmod(m.binary, 0755); err != nil {
		return fmt.Errorf("chmod binary: %w", err)
	}

	// Verify it works.
	out, err := exec.Command(m.binary, "--version").Output()
	if err != nil {
		os.Remove(m.binary)
		return fmt.Errorf("verify binary: %w", err)
	}

	m.version = strings.TrimSpace(string(out))
	log.Printf("yt-dlp: downloaded and verified version %s", m.version)
	return nil
}

// buildAssetURL returns the GitHub release URL for the current platform.
// Uses pre-compiled static binaries when available, falls back to the Python script.
func (m *Manager) buildAssetURL() string {
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return ytDlpGitHubURL + "/latest/download/yt-dlp_linux"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return ytDlpGitHubURL + "/latest/download/yt-dlp_linux_aarch64"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm":
		return ytDlpGitHubURL + "/latest/download/yt-dlp_linux_armv7l.zip"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		return ytDlpGitHubURL + "/latest/download/yt-dlp_macos"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return ytDlpGitHubURL + "/latest/download/yt-dlp_macos"
	default:
		log.Printf("yt-dlp: unsupported platform %s/%s, falling back to linux/amd64", runtime.GOOS, runtime.GOARCH)
		return ytDlpGitHubURL + "/latest/download/yt-dlp_linux"
	}
}

// extractTarGz extracts a tar.gz archive to the target directory.
func extractTarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	tarReader := tar.NewReader(gz)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		// Only extract regular files.
		if header.Typeflag != tar.TypeReg {
			continue
		}

		// Skip directories in the path.
		target := filepath.Join(dest, header.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dest)+string(os.PathSeparator)) {
			log.Printf("yt-dlp: skipping unsafe path: %s", header.Name)
			continue
		}

		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, os.FileMode(header.Mode))
		if err != nil {
			return err
		}

		if _, err := io.Copy(f, tarReader); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}
}

// findBinaryInDir searches for the yt-dlp binary in a directory tree.
func findBinaryInDir(dir, name string) (string, error) {
	var found string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Name() == name {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk for binary: %w", err)
	}
	if found == "" {
		return "", fmt.Errorf("yt-dlp binary not found in archive")
	}
	return found, nil
}

// extractFromData detects the archive type in data and extracts the yt-dlp binary.
// Supports: raw binary, tar.gz, zip, and Python scripts.
// Returns the path to the extracted binary and the temp dir (caller must clean up temp dir).
func extractFromData(data []byte, destDir string) (string, string, error) {
	tmpDir, err := os.MkdirTemp(destDir, "yt-dlp-download-*")
	if err != nil {
		return "", "", fmt.Errorf("create temp dir: %w", err)
	}

	// Check for gzip header (tar.gz).
	if isGzip(data) {
		if err := extractTarGz(bytes.NewReader(data), tmpDir); err != nil {
			os.RemoveAll(tmpDir)
			return "", "", fmt.Errorf("extract tar.gz: %w", err)
		}
		src, err := findBinaryInDir(tmpDir, ytDlpBinaryName)
		if err != nil {
			os.RemoveAll(tmpDir)
			return "", "", err
		}
		return src, tmpDir, nil
	}

	// Check for zip header.
	if isZip(data) {
		if err := extractZip(bytes.NewReader(data), tmpDir); err != nil {
			os.RemoveAll(tmpDir)
			return "", "", fmt.Errorf("extract zip: %w", err)
		}
		src, err := findBinaryInDir(tmpDir, ytDlpBinaryName)
		if err != nil {
			os.RemoveAll(tmpDir)
			return "", "", err
		}
		return src, tmpDir, nil
	}

	// Check for Python script (starts with #!).
	if isPythonScript(data) {
		src := filepath.Join(tmpDir, ytDlpBinaryName)
		if err := os.WriteFile(src, data, 0755); err != nil {
			os.RemoveAll(tmpDir)
			return "", "", fmt.Errorf("write python script: %w", err)
		}
		return src, tmpDir, nil
	}

	// Assume raw binary.
	src := filepath.Join(tmpDir, ytDlpBinaryName)
	if err := os.WriteFile(src, data, 0755); err != nil {
		os.RemoveAll(tmpDir)
		return "", "", fmt.Errorf("write binary: %w", err)
	}
	return src, tmpDir, nil
}

// isGzip checks if data starts with a gzip magic number.
func isGzip(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

// isZip checks if data starts with a zip local file header signature.
func isZip(data []byte) bool {
	return len(data) >= 4 && data[0] == 0x50 && data[1] == 0x4b && data[2] == 0x03 && data[3] == 0x04
}

// isPythonScript checks if data starts with a Python shebang.
func isPythonScript(data []byte) bool {
	return len(data) >= 2 && data[0] == '#' && data[1] == '!'
}

// extractZip extracts a zip archive to the target directory.
func extractZip(r io.Reader, dest string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read zip data: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}

		target := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dest)+string(os.PathSeparator)) {
			log.Printf("yt-dlp: skipping unsafe path: %s", f.Name)
			continue
		}

		fr, err := f.Open()
		if err != nil {
			return err
		}

		outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, f.Mode())
		if err != nil {
			fr.Close()
			return err
		}

		if _, err := io.Copy(outFile, fr); err != nil {
			outFile.Close()
			fr.Close()
			return err
		}
		outFile.Close()
		fr.Close()
	}
	return nil
}

// Update runs `yt-dlp -U` to update the binary to the latest release.
func (m *Manager) Update() error {
	m.mu.Lock()
	binary := m.binary
	m.mu.Unlock()

	log.Printf("yt-dlp: running update check...")
	cmd := exec.Command(binary, "-U")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("yt-dlp: update check output: %s", string(out))
		return fmt.Errorf("yt-dlp update: %w: %s", err, string(out))
	}

	output := strings.TrimSpace(string(out))
	log.Printf("yt-dlp: %s", output)

	// Refresh cached version.
	if v, err := exec.Command(binary, "--version").Output(); err == nil {
		m.mu.Lock()
		m.version = strings.TrimSpace(string(v))
		m.mu.Unlock()
	}

	return nil
}

// RunDailyUpdate starts a goroutine that checks for updates once per day.
// The first check happens after a 1-hour delay to avoid hitting GitHub too soon.
func (m *Manager) RunDailyUpdate(ctx context.Context) {
	go func() {
		// Initial delay before first update check.
		select {
		case <-time.After(1 * time.Hour):
		case <-ctx.Done():
			return
		}

		ticker := time.NewTicker(updateCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := m.Update(); err != nil {
					log.Printf("yt-dlp: daily update failed: %v", err)
				}
			}
		}
	}()
}

// Version returns the cached version string of the binary.
func (m *Manager) Version() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.version
}

// ---- GitHub API helpers (for version checking without downloading) ----

// GitHubRelease represents a GitHub release API response.
type GitHubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []GitHubAsset `json:"assets"`
}

// GitHubAsset represents a release asset.
type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// LatestReleaseInfo returns the latest release tag from GitHub.
func LatestReleaseInfo() (string, error) {
	resp, err := http.Get(ytDlpGitHubURL + "/latest")
	if err != nil {
		return "", fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("parse release JSON: %w", err)
	}

	return release.TagName, nil
}
