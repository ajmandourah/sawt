// Package audio manages the FFmpeg pipeline: spawning the process, reading PCM
// from stdout, slicing into 20ms frames, and sending to Mumble.
package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ladis/sawt/internal/source"
)

const (
	SampleRate    = 48000
	FrameDuration = 20 * time.Millisecond
	BitsPerSample = 16

	// BufioSize is the internal buffer size for reading FFmpeg stdout.
	BufioSize = 32 * 1024

	// FrameChannelBuffer provides ~80ms of decoupling between reader and sender.
	FrameChannelBuffer = 4
)

// Sink abstracts the audio output destination (Mumble client).
type Sink interface {
	OpenAudio()                     // open audio channel before playback
	CloseAudio()                    // close audio channel after playback
	SendAudio(samples []int16) bool // send one PCM frame; returns true if accepted
}

// Engine manages a single FFmpeg playback session.
type Engine struct {
	sink Sink

	// FFmpeg process
	cmd     *exec.Cmd
	tmpFile string
	stdout  io.ReadCloser

	// Control
	stopCh chan struct{}
	doneCh chan struct{}
	mu     sync.Mutex

	// Startup error from FFmpeg stderr
	startupErrMu sync.Mutex
	startupErr   string

	// Silence buffer
	silence []int16

	// Audio format (mono or stereo)
	channels        int
	bytesPerFrame   int
	samplesPerFrame int
}

// New creates a new Engine ready to play.
func New(sink Sink, stereo bool) *Engine {
	channels := 1
	bytesPerFrame := 1920
	samplesPerFrame := 960
	if stereo {
		channels = 2
		bytesPerFrame = 3840
		samplesPerFrame = 1920 // interleaved L/R
	}

	silence := make([]int16, samplesPerFrame)

	return &Engine{
		sink:            sink,
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
		channels:        channels,
		bytesPerFrame:   bytesPerFrame,
		samplesPerFrame: samplesPerFrame,
		silence:         silence,
	}
}

// Start spawns FFmpeg for the given source and begins streaming audio.
func (e *Engine) Start(source string) error {
	e.mu.Lock()
	if e.cmd != nil {
		e.mu.Unlock()
		return fmt.Errorf("engine already playing")
	}
	e.stopCh = make(chan struct{})
	e.doneCh = make(chan struct{})
	e.mu.Unlock()

	// Clear any startup error from a previous track.
	e.startupErrMu.Lock()
	e.startupErr = ""
	e.startupErrMu.Unlock()

	var cmd *exec.Cmd
	var stdout io.ReadCloser
	var err error
	var tmpPath string

	if strings.HasPrefix(source, "ytdlp:") {
		// yt-dlp downloads audio to a temp file, then FFmpeg reads it.
		ytURL := strings.TrimPrefix(source, "ytdlp:")
		tmpFile, err := os.CreateTemp("", "sawt-*.tmp")
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}
		tmpPath = tmpFile.Name()

		ytDlpCmd := exec.Command("yt-dlp", "-f", "ba",
			"--no-playlist", "--restrict-filenames",
			"--no-progress", "--no-warnings", "-q",
			"-o", "-", ytURL)
		ytDlpCmd.Stdout = tmpFile
		var ytDlpStderr bytes.Buffer
		ytDlpCmd.Stderr = &ytDlpStderr

		log.Printf("yt-dlp downloading: %s", ytURL)
		if err := ytDlpCmd.Run(); err != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("yt-dlp download: %w: %s", err, ytDlpStderr.String())
		}
		tmpFile.Close()

		info, err := os.Stat(tmpPath)
		if err != nil || info.Size() == 0 {
			os.Remove(tmpPath)
			return fmt.Errorf("yt-dlp produced empty file")
		}
		log.Printf("yt-dlp downloaded %d bytes", info.Size())

		// FFmpeg reads the downloaded audio file
		cmd = exec.Command("ffmpeg", "-i", tmpPath,
			"-f", "s16le", "-acodec", "pcm_s16le",
			"-ar", fmt.Sprintf("%d", SampleRate),
			"-ac", fmt.Sprintf("%d", e.channels),
			"-loglevel", "error", "-y", "-")
		stdout, err = cmd.StdoutPipe()
		if err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("ffmpeg stdout pipe: %w", err)
		}
	} else {
		// Standard: FFmpeg reads from file or URL directly.
		args := []string{
			"-i", source,
			"-f", "s16le",
			"-acodec", "pcm_s16le",
			"-ar", fmt.Sprintf("%d", SampleRate),
			"-ac", fmt.Sprintf("%d", e.channels),
			"-loglevel", "error",
			"-y",
			"-",
		}
		cmd = exec.Command("ffmpeg", args...)
		stdout, err = cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("ffmpeg stdout pipe: %w", err)
		}
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
	e.tmpFile = tmpPath
	e.stdout = stdout
	e.mu.Unlock()

	// Open audio channel for this playback session
	e.sink.OpenAudio()

	// Start playback goroutine
	go e.runLoop(stdout, &stderrBuf)

	return nil
}

// runLoop reads PCM frames from FFmpeg and sends them to Mumble.
func (e *Engine) runLoop(reader io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer func() {
		e.cleanup(reader)
		close(e.doneCh)
	}()

	// Check for FFmpeg startup errors
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

	ticker := time.NewTicker(FrameDuration)
	defer ticker.Stop()

	// Channel for passing frames from reader to sender
	pcmCh := make(chan []int16, FrameChannelBuffer)

	// Reader goroutine: read raw bytes, convert to int16, push to channel
	readerDone := make(chan struct{})
	go func() {
		defer close(pcmCh)
		defer close(readerDone)

		buf := make([]byte, BufioSize)
		offset := 0

		for {
			select {
			case <-e.stopCh:
				return
			default:
			}

			n, err := reader.Read(buf[offset:])
			offset += n

			// Extract complete frames from buffer
			for offset >= e.bytesPerFrame {
				select {
				case <-e.stopCh:
					return
				default:
				}

				// Convert s16le bytes to []int16
				samples := bytesToInt16(buf[:e.bytesPerFrame])

				select {
				case pcmCh <- samples:
				case <-e.stopCh:
					return
				}

				// Shift remaining bytes to front of buffer
				remaining := offset - e.bytesPerFrame
				if remaining > 0 {
					copy(buf, buf[e.bytesPerFrame:offset])
				}
				offset = remaining
			}

			if err != nil {
				if err != io.EOF {
					log.Printf("FFmpeg read error: %v", err)
				}
				return
			}
		}
	}()

	// Playback loop: tick at 20ms, send frames
	frameCount := 0
	for {
		select {
		case <-e.stopCh:
			<-readerDone
			log.Printf("Playback stopped after %d frames", frameCount)
			return
		case <-ticker.C:
			select {
			case pcm, ok := <-pcmCh:
				if !ok {
					// Reader finished (track ended naturally)
					log.Printf("Audio stream ended after %d frames (%.1fs)", frameCount, float64(frameCount)*0.02)
					<-readerDone
					e.waitFFmpeg()
					return
				}
				e.sink.SendAudio(pcm)
				frameCount++
			default:
				// No frame available, send silence
				e.sink.SendAudio(e.silence[:])
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

	// Remove temp file (yt-dlp downloaded audio)
	if e.tmpFile != "" {
		os.Remove(e.tmpFile)
		e.tmpFile = ""
	}

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
