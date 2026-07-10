// Package api provides the HTTP REST API for the Sawt bot's web interface.
// It wires the queue manager, audio engine, and file store into JSON endpoints
// that the Tailwind CSS frontend consumes.
package api

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ladis/sawt/internal/api/store"
	"github.com/ladis/sawt/internal/audio"
	"github.com/ladis/sawt/internal/queue"
	"github.com/ladis/sawt/internal/source"
)

//go:embed static/*
var staticFiles embed.FS

// Server is the HTTP API server.
type Server struct {
	port        int
	addr        string
	store       *store.Store
	qm          *queue.Manager
	engine      *audio.Engine
	musicDir    string
	probeCmd    string
	sourceChain *source.Chain // resolver chain for URL resolution
	mux         *http.ServeMux
}

// Config holds the dependencies needed to construct a Server.
type Config struct {
	Port        int
	Addr        string // bind address (default: ":" for all interfaces)
	Store       *store.Store
	QueueMgr    *queue.Manager
	Engine      *audio.Engine
	MusicDir    string
	ProbeCmd    string
	SourceChain *source.Chain // resolver chain for URL resolution
}

// New creates and configures the API server.
func New(cfg Config) *Server {
	addr := cfg.Addr
	if addr == "" {
		addr = "0.0.0.0" // default: listen on all interfaces
	}
	s := &Server{
		port:        cfg.Port,
		addr:        addr,
		store:       cfg.Store,
		qm:          cfg.QueueMgr,
		engine:      cfg.Engine,
		musicDir:    cfg.MusicDir,
		probeCmd:    cfg.ProbeCmd,
		sourceChain: cfg.SourceChain,
		mux:         http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

// Handler returns the http.Handler for mounting on any server.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Port returns the configured port.
func (s *Server) Port() int {
	return s.port
}

func (s *Server) registerRoutes() {
	// Static files (web UI)
	staticSub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Printf("API: failed to sub static files: %v", err)
	} else {
		s.mux.Handle("/", http.FileServer(http.FS(staticSub)))
	}

	// Library
	s.mux.HandleFunc("GET /api/library", s.handleListLibrary)
	s.mux.HandleFunc("GET /api/library/search", s.handleSearchLibrary)
	s.mux.HandleFunc("POST /api/library/url", s.handleAddURL)

	// History
	s.mux.HandleFunc("GET /api/history", s.handleGetHistory)
	s.mux.HandleFunc("POST /api/history/replay", s.handleReplayHistory)

	// Status
	s.mux.HandleFunc("GET /api/status", s.handleStatus)

	// Queue control
	s.mux.HandleFunc("GET /api/queue", s.handleGetQueue)
	s.mux.HandleFunc("POST /api/play", s.handlePlayNow)
	s.mux.HandleFunc("POST /api/queue/add", s.handleAddToQueue)
	s.mux.HandleFunc("POST /api/queue/play", s.handlePlayQueue)
	s.mux.HandleFunc("POST /api/queue/skip", s.handleSkip)
	s.mux.HandleFunc("POST /api/queue/pause", s.handlePause)
	s.mux.HandleFunc("POST /api/queue/resume", s.handleResume)
	s.mux.HandleFunc("POST /api/queue/clear", s.handleClearQueue)

	// Upload
	s.mux.HandleFunc("POST /api/upload", s.handleUpload)

	// Playlists
	s.mux.HandleFunc("GET /api/playlists", s.handleListPlaylists)
	s.mux.HandleFunc("POST /api/playlists", s.handleCreatePlaylist)
	s.mux.HandleFunc("DELETE /api/playlists/{id}", s.handleDeletePlaylist)
	s.mux.HandleFunc("POST /api/playlists/{id}/play", s.handlePlayPlaylist)
}

// Start launches the HTTP server. Blocks until context is done or server errors.
func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", s.addr, s.port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("API server listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// ---- Response Helpers ----

type jsonResponse struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, jsonResponse{OK: false, Error: msg})
}

func writeOK(w http.ResponseWriter, v any) {
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Data: v})
}

// ---- Library Handlers ----

func (s *Server) handleListLibrary(w http.ResponseWriter, r *http.Request) {
	tracks := s.store.ListTracks()
	writeOK(w, tracks)
}

func (s *Server) handleSearchLibrary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		tracks := s.store.ListTracks()
		writeOK(w, tracks)
		return
	}
	tracks := s.store.SearchTracks(q)
	writeOK(w, tracks)
}

// ---- Status Handler ----

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	state := s.qm.GetState()
	writeOK(w, map[string]any{
		"state":    state,
		"playing":  state == "playing",
		"paused":   state == "paused",
		"idle":     state == "idle",
		"queueLen": s.qm.QueueLength(),
		"engine":   map[string]bool{"running": s.engine.IsPlaying()},
	})
}

// ---- Queue Handlers ----

func (s *Server) handleGetQueue(w http.ResponseWriter, r *http.Request) {
	curr := s.qm.Current()
	items := s.qm.Queue()
	elapsed, total := s.qm.GetProgress()

	nowPlaying := map[string]any{}
	if curr != nil {
		nowPlaying = map[string]any{
			"id":          curr.Source,
			"name":        curr.Title,
			"source":      curr.Source,
			"sourceType":  curr.SourceType,
			"requestedBy": curr.RequestedBy,
			"elapsed":     int(elapsed.Seconds()),
			"total":       int(total.Seconds()),
		}
	}

	queueItems := make([]map[string]any, 0, len(items))
	for _, t := range items {
		queueItems = append(queueItems, map[string]any{
			"id":          t.Source,
			"name":        t.Title,
			"source":      t.Source,
			"sourceType":  t.SourceType,
			"requestedBy": t.RequestedBy,
		})
	}

	writeOK(w, map[string]any{
		"nowPlaying": nowPlaying,
		"queue":      queueItems,
		"state":      s.qm.GetState(),
	})
}

func (s *Server) handlePlayNow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Use source chain to resolve the input (same as Mumble chat)
	input := req.Path
	if req.ID != "" {
		track := s.store.GetTrack(req.ID)
		if track == nil {
			writeError(w, http.StatusNotFound, "track not found")
			return
		}
		input = track.Path
	}

	if input == "" {
		writeError(w, http.StatusBadRequest, "provide path or id")
		return
	}

	// Resolve through the source chain (same as Mumble !play command)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resolved, err := s.sourceChain.Resolve(ctx, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "resolve: "+err.Error())
		return
	}

	// Play now without adding to queue
	s.qm.PlayNow(&audio.Track{
		Title:       resolved.Title,
		Source:      resolved.URL,
		SourceType:  resolved.Type,
		RequestedBy: "web-ui",
	})

	// Probe duration for progress tracking
	if s.probeCmd != "" {
		_, durSec := probeDuration(resolved.URL, s.probeCmd)
		if durSec > 0 {
			s.qm.SetTrackDuration(time.Duration(durSec) * time.Second)
		}
	}

	writeOK(w, map[string]any{"name": resolved.Title, "status": "playing"})
}

func (s *Server) handleAddToQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Use source chain to resolve the input (same as Mumble chat)
	input := req.Path
	if req.ID != "" {
		track := s.store.GetTrack(req.ID)
		if track == nil {
			writeError(w, http.StatusNotFound, "track not found")
			return
		}
		input = track.Path
	}

	if input == "" {
		writeError(w, http.StatusBadRequest, "provide path or id")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resolved, err := s.sourceChain.Resolve(ctx, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "resolve: "+err.Error())
		return
	}

	t := &audio.Track{
		Title:       resolved.Title,
		Source:      resolved.URL,
		SourceType:  resolved.Type,
		RequestedBy: "web-ui",
	}
	s.qm.Enqueue(t)

	writeOK(w, map[string]any{"name": resolved.Title, "status": "enqueued"})
}

func (s *Server) handlePlayQueue(w http.ResponseWriter, r *http.Request) {
	s.qm.PlayQueue()
	writeOK(w, map[string]any{"status": "playing queue"})
}

func (s *Server) handleSkip(w http.ResponseWriter, r *http.Request) {
	s.qm.Skip()
	writeOK(w, map[string]any{"status": "skipped"})
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.qm.Pause()
	writeOK(w, map[string]any{"status": "paused"})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.qm.Resume()
	writeOK(w, map[string]any{"status": "resumed"})
}

func (s *Server) handleClearQueue(w http.ResponseWriter, r *http.Request) {
	s.qm.Stop()
	writeOK(w, map[string]any{"status": "cleared"})
}

// ---- Upload Handler ----

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}

	// Limit file size to 100MB
	r.Body = http.MaxBytesReader(w, r.Body, 100*1024*1024)

	err := r.ParseMultipartForm(100 * 1024 * 1024)
	if err != nil {
		writeError(w, http.StatusBadRequest, "parse form: "+err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "get file: "+err.Error())
		return
	}
	defer file.Close()

	// Validate extension
	ext := strings.ToLower(filepath.Ext(header.Filename))
	validExts := map[string]bool{
		".mp3": true, ".wav": true, ".ogg": true, ".flac": true, ".m4a": true,
		".wma": true, ".aac": true, ".opus": true, ".aiff": true, ".aif": true, ".alac": true,
	}
	if !validExts[ext] {
		writeError(w, http.StatusBadRequest, "unsupported file type: "+ext+" — supported: mp3, wav, ogg, flac, m4a, wma, aac, opus, aiff, aif, alac")
		return
	}

	// Detect MIME type for additional validation
	contentType := header.Header.Get("Content-Type")
	log.Printf("Upload: %s (ext=%s, content-type=%s)", header.Filename, ext, contentType)

	meta, err := s.store.SaveUpload(file, header.Filename)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "save: "+err.Error())
		return
	}

	log.Printf("Upload successful: %s (%s)", meta.Name, meta.Duration)

	writeOK(w, map[string]any{
		"uploaded": map[string]any{
			"id":       meta.ID,
			"name":     meta.Name,
			"size":     meta.Size,
			"duration": meta.Duration,
			"type":     ext,
		},
	})
}

// ---- History Handlers ----

func (s *Server) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	history := s.qm.GetHistory()
	writeOK(w, history)
}

func (s *Server) handleReplayHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Index < 0 {
		writeError(w, http.StatusBadRequest, "index must be non-negative")
		return
	}
	if !s.qm.ReplayFromHistory(req.Index) {
		writeError(w, http.StatusBadRequest, "invalid history index")
		return
	}
	writeOK(w, map[string]any{"status": "replaying"})
}

// ---- URL Handler ----

func (s *Server) handleAddURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}

	// Resolve URL through the source chain if available
	if s.sourceChain != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		resolved, err := s.sourceChain.Resolve(ctx, req.URL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "resolve: "+err.Error())
			return
		}

		trackID := fmt.Sprintf("url-%d", time.Now().UnixNano())
		track := &store.TrackMeta{
			ID:         trackID,
			Name:       resolved.Title,
			Path:       resolved.URL,
			Duration:   "0:00", // Duration will be probed later
			Seconds:    0,
			Size:       "URL",
			AddedAt:    time.Now().Format("2006-01-02"),
			SourceType: resolved.Type,
			URL:        req.URL,
		}

		// Try to extract thumbnail
		if thumb, err := extractThumbnail(req.URL, s.probeCmd); err == nil {
			track.Thumbnail = thumb
		}

		s.store.AddTrack(track)
		s.store.SaveURLs(filepath.Join(s.store.DataDir(), "urls.json"))

		writeOK(w, map[string]any{
			"added": map[string]any{
				"id":        track.ID,
				"name":      track.Name,
				"url":       track.Path,
				"type":      track.SourceType,
				"thumbnail": track.Thumbnail,
			},
		})
		return
	}

	// Fallback: simple detection without chain
	trackID := fmt.Sprintf("url-%d", time.Now().UnixNano())
	track := &store.TrackMeta{
		ID:         trackID,
		Name:       req.URL,
		Path:       req.URL,
		Duration:   "0:00",
		Seconds:    0,
		Size:       "URL",
		AddedAt:    time.Now().Format("2006-01-02"),
		SourceType: source.SourceDirect,
		URL:        req.URL,
	}

	if strings.Contains(strings.ToLower(req.URL), "youtube.com") || strings.Contains(strings.ToLower(req.URL), "youtu.be") {
		track.SourceType = source.SourceYtDlp
		track.Name = "YouTube: " + req.URL
	} else if strings.Contains(strings.ToLower(req.URL), "soundcloud.com") {
		track.SourceType = source.SourceYtDlp
		track.Name = "SoundCloud: " + req.URL
	} else if strings.Contains(strings.ToLower(req.URL), "bandcamp.com") {
		track.SourceType = source.SourceYtDlp
		track.Name = "Bandcamp: " + req.URL
	}

	s.store.AddTrack(track)
	s.store.SaveURLs(filepath.Join(s.store.DataDir(), "urls.json"))

	writeOK(w, map[string]any{
		"added": map[string]any{
			"id":   track.ID,
			"name": track.Name,
			"url":  track.Path,
			"type": track.SourceType,
		},
	})
}

// ---- Playlist Handlers ----

func (s *Server) handleListPlaylists(w http.ResponseWriter, r *http.Request) {
	pls := s.store.ListPlaylists()
	writeOK(w, pls)
}

func (s *Server) handleCreatePlaylist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string   `json:"name"`
		TrackIDs []string `json:"trackIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.TrackIDs) == 0 {
		writeError(w, http.StatusBadRequest, "at least one track is required")
		return
	}

	// Validate track IDs exist
	for _, id := range req.TrackIDs {
		if s.store.GetTrack(id) == nil {
			writeError(w, http.StatusBadRequest, "track not found: "+id)
			return
		}
	}

	p := s.store.CreatePlaylist(req.Name, req.TrackIDs)
	s.store.SavePlaylists(filepath.Join(s.store.DataDir(), "playlists.json"))
	writeOK(w, p)
}

func (s *Server) handleDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "playlist id required")
		return
	}
	ok := s.store.DeletePlaylist(id)
	if !ok {
		writeError(w, http.StatusNotFound, "playlist not found")
		return
	}
	s.store.SavePlaylists(filepath.Join(s.store.DataDir(), "playlists.json"))
	writeOK(w, map[string]any{"deleted": id})
}

func (s *Server) handlePlayPlaylist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "playlist id required")
		return
	}
	p := s.store.GetPlaylist(id)
	if p == nil {
		writeError(w, http.StatusNotFound, "playlist not found")
		return
	}

	tracks := s.store.GetPlaylistTracks(p)
	if len(tracks) == 0 {
		writeError(w, http.StatusBadRequest, "playlist has no valid tracks")
		return
	}

	// Enqueue all tracks using source chain (same as Mumble chat)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, t := range tracks {
		resolved, err := s.sourceChain.Resolve(ctx, t.Path)
		if err != nil {
			log.Printf("Failed to resolve track %s: %v", t.Name, err)
			continue
		}
		track := &audio.Track{
			Title:       resolved.Title,
			Source:      resolved.URL,
			SourceType:  resolved.Type,
			RequestedBy: "web-ui",
		}
		s.qm.Enqueue(track)
	}

	// Play the first track
	first := tracks[0]
	resolved, err := s.sourceChain.Resolve(ctx, first.Path)
	if err == nil && s.probeCmd != "" {
		_, durSec := probeDuration(resolved.URL, s.probeCmd)
		if durSec > 0 {
			s.qm.SetTrackDuration(time.Duration(durSec) * time.Second)
		}
	}

	writeOK(w, map[string]any{
		"playlist": p.Name,
		"tracks":   len(tracks),
		"status":   "playing",
	})
}

// ---- Helpers ----

// isURL checks if a string is a valid URL.
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "ftp://")
}

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

// extractThumbnail uses yt-dlp to extract the thumbnail URL for a given URL.
func extractThumbnail(url, ytDlpPath string) (string, error) {
	if ytDlpPath == "" {
		return "", fmt.Errorf("yt-dlp path not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ytDlpPath, "--print", "thumbnail", "--skip-download", url)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	thumb := strings.TrimSpace(string(out))
	if thumb == "" {
		return "", fmt.Errorf("no thumbnail found")
	}

	return thumb, nil
}

func formatDuration(totalSecs int) string {
	m := totalSecs / 60
	s := totalSecs % 60
	return fmt.Sprintf("%d:%02d", m, s)
}
