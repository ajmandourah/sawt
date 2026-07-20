// Package store manages in-memory state for the HTTP API: library metadata,
// playlists, and uploaded files. It scans the music directory on startup and
// provides CRUD operations for playlists.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ladis/sawt/internal/source"
)

// audioExts are the file extensions the store recognizes as audio.
var audioExts = map[string]bool{
	".mp3": true, ".wav": true, ".flac": true, ".ogg": true,
	".m4a": true, ".wma": true, ".aac": true, ".opus": true,
	".aiff": true, ".aif": true, ".alac": true,
}

// TrackMeta is a lightweight representation of an audio file for the API.
type TrackMeta struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Path       string            `json:"path"`
	Duration   string            `json:"duration"` // human-readable "3:42"
	Seconds    int               `json:"seconds"`  // total seconds for progress calculation
	Size       string            `json:"size"`     // human-readable "6.2 MB"
	AddedAt    string            `json:"addedAt"`
	SourceType source.SourceType `json:"sourceType"`
	Thumbnail  string            `json:"thumbnail,omitempty"` // URL or local path to album art
	URL        string            `json:"url,omitempty"`       // original URL for stream tracks
}

// Playlist is a saved collection of tracks.
type Playlist struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	TrackIDs  []string `json:"trackIds"`
	CreatedAt string   `json:"createdAt"`
}

// Store holds all in-memory state for the API layer.
type Store struct {
	mu          sync.RWMutex
	musicDir    string
	tracks      map[string]*TrackMeta // id -> track
	trackOrder  []string              // ordered list of track IDs
	playlists   map[string]*Playlist  // id -> playlist
	playlistSeq int                   // monotonic counter for ID generation
	probeCmd    string                // path to ffprobe binary
	dataDir     string                // directory for persistence files
}

// New creates a new Store, scanning the music directory for audio files.
func New(musicDir, ffprobePath, dataDir string) *Store {
	s := &Store{
		musicDir:  musicDir,
		tracks:    make(map[string]*TrackMeta),
		playlists: make(map[string]*Playlist),
		probeCmd:  ffprobePath,
		dataDir:   dataDir,
	}
	s.scanDirectory()
	s.loadURLs()
	log.Printf("Store: loaded %d tracks from %s", len(s.tracks), musicDir)
	return s
}

// scanDirectory walks MusicDir and populates the track map.
func (s *Store) scanDirectory() {
	abs, err := filepath.Abs(s.musicDir)
	if err != nil {
		log.Printf("Store: cannot resolve music dir %s: %v", s.musicDir, err)
		return
	}

	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		log.Printf("Store: music dir %s does not exist or is not a directory", abs)
		return
	}

	var ids []string
	err = filepath.Walk(abs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !audioExts[ext] {
			return nil
		}

		// Get duration via ffprobe if available
		durStr, durSec := probeDuration(path, s.probeCmd)

		meta := &TrackMeta{
			ID:         filepath.Base(path), // use filename as ID (unique in a flat dir)
			Name:       filepath.Base(path),
			Path:       path,
			Duration:   durStr,
			Seconds:    durSec,
			Size:       formatSize(info.Size()),
			AddedAt:    info.ModTime().Format("2006-01-02"),
			SourceType: source.SourceLocal,
		}
		s.tracks[meta.ID] = meta
		ids = append(ids, meta.ID)
		return nil
	})
	if err != nil {
		log.Printf("Store: error walking music dir: %v", err)
		return
	}

	// Sort by name for consistent ordering
	sort.Slice(ids, func(i, j int) bool {
		return s.tracks[ids[i]].Name < s.tracks[ids[j]].Name
	})
	s.trackOrder = ids
}

// ListTracks returns all tracks in sorted order.
func (s *Store) ListTracks() []*TrackMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*TrackMeta, 0, len(s.trackOrder))
	for _, id := range s.trackOrder {
		result = append(result, s.tracks[id])
	}
	return result
}

// GetTrack returns a track by ID.
func (s *Store) GetTrack(id string) *TrackMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tracks[id]
}

// SearchTracks returns tracks matching the query (case-insensitive name match).
func (s *Store) SearchTracks(query string) []*TrackMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q := strings.ToLower(query)
	result := make([]*TrackMeta, 0)
	for _, id := range s.trackOrder {
		t := s.tracks[id]
		if strings.Contains(strings.ToLower(t.Name), q) {
			result = append(result, t)
		}
	}
	return result
}

// AddTrack adds a new track to the store (used after upload).
func (s *Store) AddTrack(meta *TrackMeta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracks[meta.ID] = meta
	s.trackOrder = append(s.trackOrder, meta.ID)
	log.Printf("Store: added track %s", meta.Name)
}

// RemoveTrack removes a track from the store (used if file is deleted).
func (s *Store) RemoveTrack(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tracks, id)
	newOrder := make([]string, 0, len(s.trackOrder))
	for _, oid := range s.trackOrder {
		if oid != id {
			newOrder = append(newOrder, oid)
		}
	}
	s.trackOrder = newOrder
}

// RebuildOrder re-sorts the track order by name.
func (s *Store) RebuildOrder() {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.trackOrder))
	for id := range s.tracks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return s.tracks[ids[i]].Name < s.tracks[ids[j]].Name
	})
	s.trackOrder = ids
}

// ---- Playlist CRUD ----

// ListPlaylists returns all playlists.
func (s *Store) ListPlaylists() []*Playlist {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Playlist, 0, len(s.playlists))
	for _, p := range s.playlists {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// GetPlaylist returns a playlist by ID.
func (s *Store) GetPlaylist(id string) *Playlist {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.playlists[id]
}

// CreatePlaylist creates a new playlist with the given name and track IDs.
func (s *Store) CreatePlaylist(name string, trackIDs []string) *Playlist {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.playlistSeq++
	p := &Playlist{
		ID:        fmt.Sprintf("p%d", s.playlistSeq),
		Name:      name,
		TrackIDs:  trackIDs,
		CreatedAt: time.Now().Format("2006-01-02"),
	}
	s.playlists[p.ID] = p
	log.Printf("Store: created playlist %q with %d tracks", p.Name, len(p.TrackIDs))
	return p
}

// DeletePlaylist removes a playlist by ID.
func (s *Store) DeletePlaylist(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.playlists[id]; !ok {
		return false
	}
	delete(s.playlists, id)
	log.Printf("Store: deleted playlist %s", id)
	return true
}

// GetPlaylistTracks returns the TrackMeta for each track in a playlist.
func (s *Store) GetPlaylistTracks(p *Playlist) []*TrackMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*TrackMeta, 0, len(p.TrackIDs))
	for _, id := range p.TrackIDs {
		if t, ok := s.tracks[id]; ok {
			result = append(result, t)
		}
	}
	return result
}

// ---- Upload ----

// SaveUpload writes an uploaded file to the music directory and returns its metadata.
func (s *Store) SaveUpload(r io.Reader, filename string) (*TrackMeta, error) {
	abs, err := filepath.Abs(s.musicDir)
	if err != nil {
		return nil, fmt.Errorf("resolve music dir: %w", err)
	}

	// Sanitize filename
	safeName := sanitizeFilename(filename)
	dest := filepath.Join(abs, safeName)

	// Handle collisions
	if _, err := os.Stat(dest); err == nil {
		base := strings.TrimSuffix(safeName, filepath.Ext(safeName))
		ext := filepath.Ext(safeName)
		for i := 1; i < 100; i++ {
			cand := filepath.Join(abs, fmt.Sprintf("%s_%d%s", base, i, ext))
			if _, err := os.Stat(cand); err != nil {
				dest = cand
				break
			}
		}
	}

	f, err := os.Create(dest)
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	written, err := io.Copy(f, r)
	if err != nil {
		os.Remove(dest)
		return nil, fmt.Errorf("write file: %w", err)
	}

	// Probe duration
	durStr, durSec := probeDuration(dest, s.probeCmd)

	meta := &TrackMeta{
		ID:         filepath.Base(dest),
		Name:       filepath.Base(dest),
		Path:       dest,
		Duration:   durStr,
		Seconds:    durSec,
		Size:       formatSize(written),
		AddedAt:    time.Now().Format("2006-01-02"),
		SourceType: source.SourceLocal,
	}
	s.AddTrack(meta)
	return meta, nil
}

// ---- Playlist Persistence ----

// SavePlaylists writes all playlists to a JSON file.
func (s *Store) SavePlaylists(path string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := json.MarshalIndent(s.playlists, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// LoadPlaylists reads playlists from a JSON file.
func (s *Store) LoadPlaylists(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no file yet, that's fine
		}
		return err
	}
	// Handle empty file
	if len(data) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var playlists map[string]*Playlist
	if err := json.Unmarshal(data, &playlists); err != nil {
		log.Printf("Store: corrupt playlists file %s, resetting: %v", path, err)
		return nil // Don't fail, just reset
	}
	s.playlists = playlists
	// Update sequence counter
	for _, p := range playlists {
		var n int
		fmt.Sscanf(p.ID, "p%d", &n)
		if n >= s.playlistSeq {
			s.playlistSeq = n + 1
		}
	}
	log.Printf("Store: loaded %d playlists from %s", len(s.playlists), path)
	return nil
}

// ---- URL Persistence ----

// SaveURLs writes all URL tracks to a JSON file.
func (s *Store) SaveURLs(path string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Filter only URL tracks
	urlTracks := make([]*TrackMeta, 0)
	for _, t := range s.tracks {
		if t.SourceType == source.SourceYtDlp || t.SourceType == source.SourceDirect {
			urlTracks = append(urlTracks, t)
		}
	}
	data, err := json.MarshalIndent(urlTracks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// LoadURLs reads URL tracks from a JSON file.
func (s *Store) LoadURLs(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// Handle empty file
	if len(data) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var urlTracks []*TrackMeta
	if err := json.Unmarshal(data, &urlTracks); err != nil {
		log.Printf("Store: corrupt URLs file %s, resetting: %v", path, err)
		return nil // Don't fail, just reset
	}
	for _, t := range urlTracks {
		if _, exists := s.tracks[t.ID]; !exists {
			s.tracks[t.ID] = t
			s.trackOrder = append(s.trackOrder, t.ID)
		}
	}
	log.Printf("Store: loaded %d URLs from %s", len(urlTracks), path)
	return nil
}

// loadURLs loads URL tracks from the data directory.
func (s *Store) loadURLs() {
	if s.dataDir == "" {
		return
	}
	urlPath := filepath.Join(s.dataDir, "urls.json")
	if err := s.LoadURLs(urlPath); err != nil {
		log.Printf("Store: failed to load URLs: %v", err)
	}
}

// DataDir returns the data directory for persistence files.
func (s *Store) DataDir() string {
	return s.dataDir
}

// ---- Helpers ----

// probeDuration uses ffprobe to get the duration of an audio file.
// Returns (human-readable "3:42", seconds).
func probeDuration(path, probeCmd string) (string, int) {
	if probeCmd == "" {
		return "0:00", 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, probeCmd, "-v", "quiet", "-show_entries", "format=duration", "-of", "json", path)
	out, err := cmd.Output()
	if err != nil {
		return "0:00", 0
	}

	var result struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &result); err != nil || result.Format.Duration == "" {
		return "0:00", 0
	}

	var secs float64
	fmt.Sscanf(result.Format.Duration, "%f", &secs)
	return formatDuration(int(secs)), int(secs)
}

func formatDuration(totalSecs int) string {
	m := totalSecs / 60
	s := totalSecs % 60
	return fmt.Sprintf("%d:%02d", m, s)
}

func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
	)
	switch {
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func sanitizeFilename(name string) string {
	// Replace characters that are problematic on filesystems
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_",
		"*", "_", "?", "_", "\"", "_",
		"<", "_", ">", "_", "|", "_",
	)
	name = replacer.Replace(name)
	// Trim leading/trailing whitespace
	name = strings.TrimSpace(name)
	if name == "" {
		name = "upload"
	}
	return name
}
