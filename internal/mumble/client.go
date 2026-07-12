// Package mumble wraps the gumble library to provide Mumble connectivity,
// authentication, channel joining, and text message dispatch.
package mumble

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"layeh.com/gopus"
	"layeh.com/gumble/gumble"
	"layeh.com/gumble/gumbleutil"
	_ "layeh.com/gumble/opus" // register Opus codec

	"github.com/ladis/sawt/internal/config"
)

// TextHandler is called when a text message is received on the Mumble server.
type TextHandler func(user *gumble.User, message string)

// Client wraps a gumble connection with reconnection logic and message dispatch.
type Client struct {
	cfg       *config.Config
	client    *gumble.Client
	handler   TextHandler
	connected chan struct{} // closed when the Mumble connection is established
	mu        sync.Mutex
	stopped   bool

	// Audio output channel (one per playback session)
	audioCh chan<- gumble.AudioBuffer
}

// StereoEncoder wraps gopus with 2-channel support for stereo audio.
type StereoEncoder struct {
	enc *gopus.Encoder
}

func (e *StereoEncoder) ID() int { return 4 }

func (e *StereoEncoder) Encode(pcm []int16, frameSize, maxDataBytes int) ([]byte, error) {
	if e.enc == nil {
		return nil, fmt.Errorf("encoder is nil")
	}

	// gumble passes total samples as frameSize, but Opus expects samples per channel
	// For stereo interleaved: frameSize should be len(pcm)/2
	correctedFrameSize := frameSize / 2
	if correctedFrameSize <= 0 {
		correctedFrameSize = 1
	}

	return e.enc.Encode(pcm, correctedFrameSize, maxDataBytes)
}

func (e *StereoEncoder) Reset() {
	e.enc.ResetState()
}

// New creates a new Mumble client and connects to the server.
// Does NOT join any channel — call JoinChannel() after full initialization.
func New(cfg *config.Config) (*Client, error) {
	c := &Client{
		cfg:       cfg,
		connected: make(chan struct{}),
	}

	if err := c.connect(); err != nil {
		return nil, fmt.Errorf("mumble connect: %w", err)
	}

	// Wait for connection only (no channel join yet)
	select {
	case <-c.connected:
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("mumble connect: timed out waiting for connection")
	}

	log.Printf("Connected to Mumble: %s as %s", cfg.Server, cfg.Username)
	return c, nil
}

func (c *Client) connect() error {
	// Reset signaling channel for each connect attempt (handles reconnects).
	c.mu.Lock()
	c.connected = make(chan struct{})
	c.mu.Unlock()

	gConfig := gumble.NewConfig()
	gConfig.Username = c.cfg.Username
	gConfig.Password = c.cfg.Password

	// Attach auto-bitrate listener
	gConfig.Attach(gumbleutil.AutoBitrate)

	// Create stereo encoder if needed (will be re-applied after codec config)
	var stereoEnc *StereoEncoder
	if c.cfg.Stereo {
		log.Printf("Creating stereo Opus encoder...")
		enc, err := gopus.NewEncoder(gumble.AudioSampleRate, 2, gopus.Voip)
		if err != nil {
			log.Printf("ERROR: Failed to create stereo Opus encoder: %v", err)
		} else {
			enc.SetBitrate(gopus.BitrateMaximum)
			stereoEnc = &StereoEncoder{enc: enc}
			log.Printf("Successfully created stereo Opus encoder")
		}
	}

	// Attach our event listener
	gConfig.Attach(gumbleutil.Listener{
		Connect: func(e *gumble.ConnectEvent) {
			log.Printf("Mumble connected")

			// Re-apply stereo encoder (codec config may have overwritten it)
			if c.cfg.Stereo {
				if stereoEnc == nil {
					log.Printf("ERROR: stereoEnc is nil!")
				} else if stereoEnc.enc == nil {
					log.Printf("ERROR: stereoEnc.enc is nil (encoder creation failed)!")
				} else {
					e.Client.AudioEncoder = stereoEnc
					log.Printf("Re-applied stereo encoder after connect")
				}
			}

			// Signal that the basic connection is established.
			// Channel join is deferred to JoinChannel() after full initialization.
			c.mu.Lock()
			select {
			case <-c.connected:
			default:
				close(c.connected)
			}
			c.mu.Unlock()
		},

		Disconnect: func(e *gumble.DisconnectEvent) {
			log.Printf("Mumble disconnected: %v (%s)", e.Type, e.String)
			go c.reconnect()
		},
		TextMessage: func(e *gumble.TextMessageEvent) {
			c.handleTextMessage(e)
		},
	})

	// Build TLS config
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true, // We'll add proper verification later
	}

	// Load client certificate if configured
	if c.cfg.TLSCert != "" && c.cfg.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(c.cfg.TLSCert, c.cfg.TLSKey)
		if err != nil {
			return fmt.Errorf("load TLS cert: %w", err)
		}
		tlsConfig.Certificates = append(tlsConfig.Certificates, cert)
	}

	client, err := gumble.DialWithDialer(new(net.Dialer), c.cfg.Server, gConfig, tlsConfig)
	if err != nil {
		return fmt.Errorf("gumble dial: %w", err)
	}

	c.client = client
	return nil
}

func (c *Client) reconnect() {
	delay := time.Second
	for {
		c.mu.Lock()
		if c.stopped {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		log.Printf("Reconnecting in %v...", delay)
		time.Sleep(delay)

		err := c.connect()
		if err == nil {
			log.Printf("Reconnected to Mumble")
			// Rejoin the configured channel after reconnect.
			c.JoinChannel(c.cfg.Channel)
			return
		}

		log.Printf("Reconnect failed: %v", err)
		delay *= 2
		if delay > time.Minute {
			delay = time.Minute
		}
	}
}

func (c *Client) handleTextMessage(e *gumble.TextMessageEvent) {
	if c.handler == nil {
		return
	}

	// Channel messages: e.Sender is set but Users/Channels may be empty
	if e.Sender != nil && e.Sender != c.client.Self {
		log.Printf("Text from %s: %q", e.Sender.Name, e.Message)
		c.handler(e.Sender, e.TextMessage.Message)
		return
	}

	// Direct messages: target specific users
	for _, user := range e.TextMessage.Users {
		if user == c.client.Self {
			continue // ignore our own messages
		}
		c.handler(user, e.TextMessage.Message)
	}
}

// JoinChannel moves the bot to the named channel.
// Call this AFTER all initialization is complete (after "Sawt is online").
// Blocks until the server confirms the move, or the channel doesn't exist.
func (c *Client) JoinChannel(name string) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()

	if client == nil {
		log.Printf("JoinChannel: client is nil")
		return
	}

	log.Printf("Joining channel: %q", name)
	log.Printf("Self: session=%d name=%q channel=%q", client.Self.Session, client.Self.Name, client.Self.Channel.Name)

	ch := client.Channels.Find(name)
	if ch == nil {
		log.Printf("Channel %q not found — staying in %s (ID=%d)", name, client.Self.Channel.Name, client.Self.Channel.ID)
		return
	}

	// Use a channel to wait for the server's UserState confirmation.
	done := make(chan struct{}, 1)

	// Attach a one-shot listener for the confirmation.
	client.Config.Attach(gumbleutil.Listener{
		UserChange: func(e *gumble.UserChangeEvent) {
			if e.User != nil && e.User.Session == client.Self.Session && e.Type.Has(gumble.UserChangeChannel) {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		},
	})

	client.Self.Move(ch)
	log.Printf("Sent Move to %s (ID=%d), waiting for server confirmation", ch.Name, ch.ID)

	select {
	case <-done:
		log.Printf("Joined channel: %s (ID=%d)", ch.Name, ch.ID)
	case <-time.After(5 * time.Second):
		log.Printf("Timeout waiting for channel join confirmation to %s", name)
	}
}

// SetTextHandler registers the function called for incoming text messages.
func (c *Client) SetTextHandler(h TextHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handler = h
}

// Self returns the bot's own user object.
func (c *Client) Self() *gumble.User {
	if c.client == nil {
		return nil
	}
	return c.client.Self
}

// SendMessage sends a text message to the current channel.
func (c *Client) SendMessage(msg string) {
	if c.client == nil || c.client.Self == nil {
		return
	}
	if ch := c.client.Self.Channel; ch != nil {
		ch.Send(msg, false)
	}
}

// ReplyToUser sends a text message to the same channel as the target user.
func (c *Client) ReplyToUser(user *gumble.User, msg string) {
	if c.client == nil || c.client.Self == nil {
		return
	}
	log.Printf("Replying to %s: %s", user.Name, msg)
	user.Send(msg)
}

// OpenAudio opens the audio output channel for a playback session.
// Call CloseAudio() when done to flush the final frame.
func (c *Client) OpenAudio() {
	if c.client == nil {
		log.Printf("OpenAudio: client is nil!")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.audioCh = c.client.AudioOutgoing()
	log.Printf("OpenAudio: channel opened")
}

// CloseAudio closes the audio output channel, flushing the final frame.
func (c *Client) CloseAudio() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.audioCh != nil {
		close(c.audioCh)
		c.audioCh = nil
		log.Printf("CloseAudio: channel closed")
	}
}

// SendAudio sends one PCM frame via gumble's AudioOutgoing channel.
// Uses non-blocking send to avoid stalling the playback loop.
func (c *Client) SendAudio(samples []int16) bool {
	c.mu.Lock()
	audioCh := c.audioCh
	c.mu.Unlock()

	if audioCh == nil {
		return false
	}
	// Non-blocking send — drop frame if channel is full.
	select {
	case audioCh <- samples:
		return true
	default:
		return false
	}
}

// Stop gracefully shuts down the client.
func (c *Client) Stop() {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()

	if c.client != nil {
		c.client.Disconnect()
	}
}
