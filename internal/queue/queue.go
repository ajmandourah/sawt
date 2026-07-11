// Package queue manages the playback queue and coordinates with the audio engine.
package queue

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"github.com/ladis/sawt/internal/audio"
	"github.com/ladis/sawt/internal/source"
)

// State represents the current playback state.
type State int

const (
	StateIdle    State = iota
	StatePlaying State = iota
	StatePaused  State = iota
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StatePlaying:
		return "playing"
	case StatePaused:
		return "paused"
	default:
		return "unknown"
	}
}

// Manager handles the playback queue and coordinates audio playback.
type Manager struct {
	mu    sync.RWMutex
	state State
	items []*audio.Track
	curr  *audio.Track

	// Audio engine
	engine *audio.Engine

	// Progress tracking
	trackStartedAt time.Time     // when the current track started playing
	trackDuration  time.Duration // total duration of the current track (if known)

	// Pause state — saved track so Resume can restart it
	pausedTrack *audio.Track

	// History
	history []*HistoryEntry // recently played tracks

	// Callbacks
	onStateChange func(State)
	onTrackChange func(*audio.Track)
}

// HistoryEntry represents a played track in the history.
type HistoryEntry struct {
	Title       string            `json:"title"`
	Source      string            `json:"source"`
	SourceType  source.SourceType `json:"sourceType"`
	RequestedBy string            `json:"requestedBy"`
	PlayedAt    time.Time         `json:"playedAt"`
	Duration    time.Duration     `json:"duration"`
}

// New creates a new queue manager.
func New(engine *audio.Engine) *Manager {
	return &Manager{
		state:  StateIdle,
		engine: engine,
		items:  make([]*audio.Track, 0),
	}
}

// SetStateChangeCallback sets a callback for state changes.
func (m *Manager) SetStateChangeCallback(fn func(State)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onStateChange = fn
}

// SetTrackChangeCallback sets a callback for track changes.
func (m *Manager) SetTrackChangeCallback(fn func(*audio.Track)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onTrackChange = fn
}

func (m *Manager) emitStateChange() {
	if m.onStateChange != nil {
		m.onStateChange(m.state)
	}
}

func (m *Manager) emitTrackChange() {
	if m.onTrackChange != nil && m.curr != nil {
		m.onTrackChange(m.curr)
	}
}

// Enqueue adds a track to the queue without playing.
func (m *Manager) Enqueue(track *audio.Track) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.items = append(m.items, track)
}

// PlayQueue starts playing the queue from the beginning.
// If something is already playing, it stops first.
func (m *Manager) PlayQueue() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.items) == 0 {
		return
	}

	if m.state == StatePlaying {
		log.Printf("Queue: stopping current track to play queue")
		m.stopCurrent()
	}

	m.startNext()
}

// PlayNow plays a track immediately without adding to queue.
func (m *Manager) PlayNow(track *audio.Track) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stopCurrent()

	m.curr = track
	m.trackStartedAt = time.Now()
	m.trackDuration = 0

	// Prefix yt-dlp sources
	playSource := track.Source
	if track.SourceType == source.SourceYtDlp {
		playSource = "ytdlp:" + track.Source
	}

	if err := m.engine.Start(playSource); err != nil {
		log.Printf("Failed to play track %q: %v", track.Title, err)
		m.curr = nil
		return
	}

	m.state = StatePlaying
	m.emitStateChange()
	m.emitTrackChange()
}

// RestartCurrent stops and restarts the currently playing track from the beginning.
func (m *Manager) RestartCurrent() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.curr == nil {
		return
	}

	track := m.curr
	m.stopCurrent()

	m.curr = track
	m.trackStartedAt = time.Now()
	m.trackDuration = 0

	// Prefix yt-dlp sources
	playSource := track.Source
	if track.SourceType == source.SourceYtDlp {
		playSource = "ytdlp:" + track.Source
	}

	if err := m.engine.Start(playSource); err != nil {
		log.Printf("Failed to restart track %q: %v", track.Title, err)
		m.curr = nil
		return
	}

	m.state = StatePlaying
	m.emitStateChange()
	m.emitTrackChange()
}

// RemoveAt removes the track at the given index from the queue.
func (m *Manager) RemoveAt(index int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if index < 0 || index >= len(m.items) {
		return false
	}
	m.items = append(m.items[:index], m.items[index+1:]...)
	return true
}

// Current returns the currently playing track.
func (m *Manager) Current() *audio.Track {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.curr
}

// Queue returns a copy of the queue items.
func (m *Manager) Queue() []*audio.Track {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*audio.Track, len(m.items))
	copy(result, m.items)
	return result
}

// QueueLength returns the number of items in the queue.
func (m *Manager) QueueLength() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.items)
}

// State returns the current playback state.
func (m *Manager) State() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// Skip stops the current track and starts the next one.
func (m *Manager) Skip() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == StateIdle {
		return
	}

	// Stop current track
	m.stopCurrent()

	// Start next if available
	m.startNext()
}

// Stop stops playback and clears the queue.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stopCurrent()
	m.items = m.items[:0]
	m.pausedTrack = nil
	m.state = StateIdle
	m.emitStateChange()
}

// Pause pauses playback by saving the current track and stopping the engine.
func (m *Manager) Pause() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != StatePlaying {
		return
	}

	// Save current track so Resume can restart it
	m.pausedTrack = m.curr

	// Stop the engine
	m.stopCurrent()
	m.state = StatePaused
	m.emitStateChange()
}

// Resume resumes playback by restarting the saved track.
func (m *Manager) Resume() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != StatePaused {
		return
	}

	if m.pausedTrack == nil {
		m.state = StateIdle
		m.emitStateChange()
		return
	}

	// Restart the paused track
	m.curr = m.pausedTrack
	m.pausedTrack = nil
	m.trackStartedAt = time.Now()
	m.trackDuration = 0

	// Prefix yt-dlp sources
	playSource := m.curr.Source
	if m.curr.SourceType == source.SourceYtDlp {
		playSource = "ytdlp:" + m.curr.Source
	}

	if err := m.engine.Start(playSource); err != nil {
		log.Printf("Failed to resume track %q: %v", m.curr.Title, err)
		m.curr = nil
		m.state = StateIdle
		m.emitStateChange()
		return
	}

	m.state = StatePlaying
	m.emitStateChange()
	m.emitTrackChange()
}

func (m *Manager) stopCurrent() {
	if m.engine.IsPlaying() {
		m.engine.Stop()
	}
	m.curr = nil
	m.trackStartedAt = time.Time{}
	m.trackDuration = 0
}

func (m *Manager) startNext() {
	if len(m.items) == 0 {
		m.state = StateIdle
		m.emitStateChange()
		return
	}

	// Pop first item
	m.curr = m.items[0]
	m.items = m.items[1:]

	// Prefix yt-dlp sources so the engine pipes yt-dlp → FFmpeg.
	playSource := m.curr.Source
	if m.curr.SourceType == source.SourceYtDlp {
		playSource = "ytdlp:" + m.curr.Source
	}

	// Start playing
	if err := m.engine.Start(playSource); err != nil {
		log.Printf("Failed to start track %q: %v", m.curr.Title, err)
		m.curr = nil
		m.startNext() // try next
		return
	}

	// Record when this track started for progress tracking
	m.trackStartedAt = time.Now()
	m.trackDuration = 0 // default: unknown duration

	m.state = StatePlaying
	m.emitStateChange()
	m.emitTrackChange()

	// When track finishes, start next
	currentTrack := m.curr // capture before goroutine runs
	go func() {
		<-m.engine.Done()

		// Check for FFmpeg startup errors (unsupported format, corrupted file, etc.)
		startupErr := m.engine.GetStartupError()
		if startupErr != "" {
			log.Printf("Track %q failed: %s", currentTrack.Title, startupErr)
		}

		m.mu.Lock()
		defer m.mu.Unlock()

		// Don't proceed if the queue was stopped/cleared while this track was playing.
		if m.state == StateIdle {
			return
		}

		// Don't proceed if this track was skipped (replaced by a newer track).
		// The old track's Done() fires after Skip(), but we should ignore it.
		if m.curr != currentTrack {
			log.Printf("Track %q was skipped, ignoring finish event", currentTrack.Title)
			return
		}

		log.Printf("Track finished: %s", currentTrack.Title)

		// Add to history
		elapsed := time.Since(m.trackStartedAt)
		m.history = append(m.history, &HistoryEntry{
			Title:       currentTrack.Title,
			Source:      currentTrack.Source,
			SourceType:  currentTrack.SourceType,
			RequestedBy: currentTrack.RequestedBy,
			PlayedAt:    time.Now(),
			Duration:    elapsed,
		})
		// Keep only last 100 entries
		if len(m.history) > 100 {
			m.history = m.history[len(m.history)-100:]
		}

		m.stopCurrent()
		m.startNext()
	}()
}

// Clear removes all items from the queue.
func (m *Manager) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = m.items[:0]
	m.trackStartedAt = time.Time{}
	m.trackDuration = 0
}

// GetProgress returns the current playback progress as (elapsed, total) duration.
// elapsed is computed from when the track started; total is the known duration if set.
func (m *Manager) GetProgress() (elapsed time.Duration, total time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.state != StatePlaying || m.curr == nil {
		return 0, 0
	}
	elapsed = time.Since(m.trackStartedAt)
	total = m.trackDuration
	if elapsed < 0 {
		elapsed = 0
	}
	return
}

// SetTrackDuration sets the known duration for the current track (used when
// ffprobe or metadata provides it). Call before or during playback.
func (m *Manager) SetTrackDuration(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trackDuration = d
}

// GetState returns the current playback state as a string.
func (m *Manager) GetState() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.String()
}

// GetHistory returns the playback history.
func (m *Manager) GetHistory() []*HistoryEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*HistoryEntry, len(m.history))
	copy(result, m.history)
	return result
}

// SaveHistory writes the history to a JSON file.
func (m *Manager) SaveHistory(path string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, err := json.MarshalIndent(m.history, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// LoadHistory reads history from a JSON file.
func (m *Manager) LoadHistory(path string) error {
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
	m.mu.Lock()
	defer m.mu.Unlock()
	var history []*HistoryEntry
	if err := json.Unmarshal(data, &history); err != nil {
		log.Printf("Queue: corrupt history file %s, resetting: %v", path, err)
		return nil // Don't fail, just reset
	}
	m.history = history
	log.Printf("Queue: loaded %d history entries from %s", len(m.history), path)
	return nil
}

// ReplayFromHistory replays a track from history by index.
func (m *Manager) ReplayFromHistory(index int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if index < 0 || index >= len(m.history) {
		return false
	}
	hEntry := m.history[index]
	t := &audio.Track{
		Title:       hEntry.Title,
		Source:      hEntry.Source,
		SourceType:  hEntry.SourceType,
		RequestedBy: hEntry.RequestedBy,
	}
	m.stopCurrent()
	m.items = append([]*audio.Track{t}, m.items...)
	m.startNext()
	return true
}
