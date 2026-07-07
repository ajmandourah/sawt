// Package audio manages the FFmpeg pipeline: spawning the process, reading PCM
// from stdout, slicing into 20ms frames, and sending to Mumble.
package audio

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/ladis/sawt/internal/source"
	"layeh.com/gopus"
	"layeh.com/gumble/gumble"
)

const (
	// Audio format constants for Mumble compatibility.
	// Mumble uses mono audio (1 channel) per the gumble library.
	SampleRate    = 48000
	Channels      = 1
	BitsPerSample = 16
	FrameDuration = 20 * time.Millisecond

	// BytesPerFrame = SampleRate × Channels × (BitsPerSample/8) × FrameDuration
	// = 48000 × 1 × 2 × 0.020 = 1920
	BytesPerFrame = 1920

	// SamplesPerFrame = BytesPerFrame / 2 (each int16 is 2 bytes)
	// = 1920 / 2 = 960 samples per frame
	SamplesPerFrame = 960

	// BufioSize is the internal buffer size for reading FFmpeg stdout.
	BufioSize = 32 * 1024

	// FrameChannelBuffer provides ~80ms of decoupling between reader and encoder.
	FrameChannelBuffer = 4
)

// Sink abstracts the audio output destination (Mumble client).
type Sink interface {
	OpenAudio()                           // open audio channel before playback
	CloseAudio()                          // close audio channel after playback
	SendOpus(data []byte, seq int64) bool // send pre-encoded Opus packet; returns true if accepted
}

// Engine manages a single FFmpeg playback session.
type Engine struct {
	sink Sink

	// FFmpeg process
	cmd    *exec.Cmd
	stdout io.ReadCloser

	// Control
	stopCh chan struct{} // closed to signal stop
	doneCh chan struct{} // closed when engine fully shut down
	mu     sync.Mutex

	// Startup error from FFmpeg stderr (set during first few seconds)
	startupErrMu sync.Mutex
	startupErr   string

	// Silence buffer (pre-allocated int16 samples)
	silence [SamplesPerFrame]int16
}

// New creates a new Engine ready to play.
func New(sink Sink) *Engine {
	return &Engine{
		sink:   sink,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start spawns FFmpeg for the given source and begins streaming audio.
// It returns immediately; playback runs in the background.
// Call Stop() to terminate playback.
func (e *Engine) Start(source string) error {
	e.mu.Lock()
	if e.cmd != nil {
		e.mu.Unlock()
		return fmt.Errorf("engine already playing")
	}
	e.stopCh = make(chan struct{})
	e.doneCh = make(chan struct{})
	e.mu.Unlock()

	// Build FFmpeg command
	args := []string{
		"-i", source,
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-ar", fmt.Sprintf("%d", SampleRate),
		"-ac", fmt.Sprintf("%d", Channels),
		"-loglevel", "error",
		"-y", // overwrite output
		"-",  // write to stdout
	}

	cmd := exec.Command("ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ffmpeg stdout pipe: %w", err)
	}

	// Capture stderr for error reporting
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		stdout.Close()
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	e.mu.Lock()
	e.cmd = cmd
	e.stdout = stdout
	e.mu.Unlock()

	// Open audio channel for this playback session
	e.sink.OpenAudio()

	// Start playback goroutine
	go e.runLoop(stdout, &stderrBuf)

	return nil
}

// runLoop reads PCM frames from FFmpeg, encodes to Opus, and sends to Mumble.
// Uses a reader goroutine with a large buffer channel so network latency
// never blocks the send loop. When the channel is empty, the last good frame
// is repeated to avoid audible gaps.
func (e *Engine) runLoop(reader io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer func() {
		e.cleanup(reader)
		close(e.doneCh)
	}()

	// Check for FFmpeg startup errors (file not found, unsupported format, etc.)
	go func() {
		time.Sleep(2 * time.Second)
		e.startupErrMu.Lock()
		errStr := stderrBuf.String()
		e.startupErrMu.Unlock()
		if errStr != "" {
			e.mu.Lock()
			cmd := e.cmd
			e.mu.Unlock()
			if cmd == nil || cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				e.startupErrMu.Lock()
				e.startupErr = errStr
				e.startupErrMu.Unlock()
				log.Printf("FFmpeg startup error: %s", errStr)
				select {
				case <-e.stopCh:
				default:
					close(e.stopCh)
				}
			}
		}
	}()

	// Create our own Opus encoder (mono, 48kHz)
	encoder, err := gopus.NewEncoder(gumble.AudioSampleRate, gumble.AudioChannels, gopus.Voip)
	if err != nil {
		log.Printf("Opus encoder creation failed: %v", err)
		return
	}
	encoder.SetBitrate(gopus.BitrateMaximum)

	// Buffered channel absorbs bursty FFmpeg output.
	// 200 frames = ~4 seconds of headroom for network streams.
	frameCh := make(chan []int16, 200)

	// Reader goroutine: read PCM from FFmpeg, convert to int16, push to channel.
	readerDone := make(chan struct{})
	go func() {
		defer close(frameCh)
		defer close(readerDone)

		br := bufio.NewReaderSize(reader, BufioSize)
		pcmBuf := make([]byte, BytesPerFrame)

		for {
			_, err := io.ReadFull(br, pcmBuf)
			if err != nil {
				return // EOF or error — channel close will signal sender
			}

			samples := bytesToInt16(pcmBuf)
			select {
			case frameCh <- samples:
			case <-e.stopCh:
				return
			default:
				// Channel full — drop to avoid blocking the reader.
				// The sender will repeat the last good frame.
			}
		}
	}()

	ticker := time.NewTicker(FrameDuration)
	defer ticker.Stop()

	var lastOpus []byte // cache last good Opus frame to repeat on gap
	frameCount := 0
	dropped := 0
	seq := int64(0)
	finished := false

	for {
		select {
		case <-e.stopCh:
			if dropped > 0 {
				log.Printf("Playback stopped after %d frames (%d dropped)", frameCount, dropped)
			} else {
				log.Printf("Playback stopped after %d frames", frameCount)
			}
			return

		case <-readerDone:
			// Reader finished — mark done. Next tick will exit.
			finished = true

		case samples, ok := <-frameCh:
			if !ok {
				// Channel closed (reader finished)
				finished = true
				continue
			}
			// Got a fresh frame — encode and send
			opusData, encErr := encoder.Encode(samples, SamplesPerFrame, 4096)
			if encErr != nil || len(opusData) == 0 {
				continue
			}
			lastOpus = opusData

			if !e.sink.SendOpus(opusData, seq) {
				dropped++
				continue
			}
			seq++
			frameCount++

		case <-ticker.C:
			if finished {
				log.Printf("Audio stream ended after %d frames", frameCount)
				e.waitFFmpeg()
				return
			}
			// No fresh frame available — repeat last good frame to avoid gap.
			if len(lastOpus) > 0 {
				if !e.sink.SendOpus(lastOpus, seq) {
					dropped++
					continue
				}
				seq++
				frameCount++
			}
		}
	}
}

// bytesToInt16 converts s16le bytes to []int16.
func bytesToInt16(b []byte) []int16 {
	n := len(b) / 2
	samples := make([]int16, n)
	for i := 0; i < n; i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(b[i*2 : i*2+2]))
	}
	return samples
}

// cleanup stops the FFmpeg process and closes resources.
func (e *Engine) cleanup(reader io.ReadCloser) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Close audio channel to flush final frame
	e.sink.CloseAudio()

	if e.cmd != nil {
		if err := e.cmd.Process.Kill(); err != nil {
			log.Printf("Kill FFmpeg: %v", err)
		}
		e.cmd.Wait() // reap zombie
		e.cmd = nil
	}

	if reader != nil {
		reader.Close()
	}
}

// waitFFmpeg waits for the FFmpeg process to exit (natural end of stream).
func (e *Engine) waitFFmpeg() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cmd != nil && e.cmd.Process != nil {
		e.cmd.Wait()
		e.cmd = nil
	}
}

// Stop terminates playback immediately.
// It blocks until the engine is fully shut down.
func (e *Engine) Stop() {
	e.mu.Lock()
	if e.cmd == nil {
		e.mu.Unlock()
		return // already stopped
	}
	e.mu.Unlock()

	close(e.stopCh)
	<-e.doneCh // wait for full shutdown
}

// Done returns a channel that is closed when playback finishes naturally
// (track ends) or is stopped.
func (e *Engine) Done() <-chan struct{} {
	return e.doneCh
}

// IsPlaying reports whether FFmpeg is currently running.
func (e *Engine) IsPlaying() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cmd != nil
}

// isSendingAudio reports whether we've sent any audio frames yet.
// Used to distinguish startup failures from mid-playback errors.
func (e *Engine) isSendingAudio() bool {
	// If FFmpeg stdout has produced any data, we're past startup
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stdout != nil
}

// GetStartupError returns any FFmpeg startup error captured during
// the first few seconds of playback. Empty string means no error.
func (e *Engine) GetStartupError() string {
	e.startupErrMu.Lock()
	defer e.startupErrMu.Unlock()
	return e.startupErr
}

// Track represents a playable audio source.
type Track struct {
	Title       string
	Source      string            // resolved URL or file path
	SourceType  source.SourceType // how the source was obtained
	RequestedBy string            // Mumble username who requested
}
