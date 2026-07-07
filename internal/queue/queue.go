// Package queue manages the playback queue and coordinates with the audio engine.
package queue

import (
	"log"
	"sync"

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
	mu    sync.Mutex
	state State
	items []*audio.Track
	curr  *audio.Track

	// Audio engine
	engine *audio.Engine

	// Callbacks
	onStateChange func(State)
	onTrackChange func(*audio.Track)
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

// Enqueue adds a track to the queue.
// If nothing is playing, it starts playing immediately.
func (m *Manager) Enqueue(track *audio.Track) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.items = append(m.items, track)

	if m.state == StateIdle {
		m.startNext()
	}
}

// Current returns the currently playing track.
func (m *Manager) Current() *audio.Track {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.curr
}

// Queue returns a copy of the queue items.
func (m *Manager) Queue() []*audio.Track {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*audio.Track, len(m.items))
	copy(result, m.items)
	return result
}

// QueueLength returns the number of items in the queue.
func (m *Manager) QueueLength() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

// State returns the current playback state.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.state = StateIdle
	m.emitStateChange()
}

// Pause pauses playback.
func (m *Manager) Pause() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != StatePlaying {
		return
	}

	// For now, pause = stop current and mark as paused
	// True pause (FFmpeg -ss seek) comes later
	m.stopCurrent()
	m.state = StatePaused
	m.emitStateChange()
}

// Resume resumes playback.
func (m *Manager) Resume() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != StatePaused {
		return
	}

	if m.curr != nil {
		// Re-start the current track
		m.startNext()
	}
}

func (m *Manager) stopCurrent() {
	if m.engine.IsPlaying() {
		m.engine.Stop()
	}
	m.curr = nil
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

		log.Printf("Track finished: %s", currentTrack.Title)
		m.stopCurrent()
		m.startNext()
	}()
}

// Clear removes all items from the queue.
func (m *Manager) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = m.items[:0]
}
