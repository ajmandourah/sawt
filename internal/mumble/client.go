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

	"layeh.com/gumble/gumble"
	"layeh.com/gumble/gumbleutil"
	_ "layeh.com/gumble/opus" // register Opus codec

	"github.com/ladis/sawt/internal/config"
)

// TextHandler is called when a text message is received on the Mumble server.
type TextHandler func(user *gumble.User, message string)

// Client wraps a gumble connection with reconnection logic and message dispatch.
type Client struct {
	cfg     *config.Config
	client  *gumble.Client
	handler TextHandler
	ready   chan struct{} // closed when connected and ready
	mu      sync.Mutex
	stopped bool

	// Audio output channel (one per playback session)
	audioCh chan<- gumble.AudioBuffer
}

// New creates a new Mumble client and connects to the server.
func New(cfg *config.Config) (*Client, error) {
	c := &Client{
		cfg:   cfg,
		ready: make(chan struct{}),
	}

	if err := c.connect(); err != nil {
		return nil, fmt.Errorf("mumble connect: %w", err)
	}

	// Wait for ready signal
	select {
	case <-c.ready:
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("mumble connect: timed out waiting for ready")
	}

	log.Printf("Connected to Mumble: %s as %s", cfg.Server, cfg.Username)
	return c, nil
}

func (c *Client) connect() error {
	gConfig := gumble.NewConfig()
	gConfig.Username = c.cfg.Username
	gConfig.Password = c.cfg.Password

	// Attach auto-bitrate listener
	gConfig.Attach(gumbleutil.AutoBitrate)

	// Attach our event listener
	gConfig.Attach(gumbleutil.Listener{
		Connect: func(e *gumble.ConnectEvent) {
			log.Printf("Mumble connected, joining channel: %s", c.cfg.Channel)
			c.joinChannel(c.cfg.Channel)
			c.mu.Lock()
			select {
			case <-c.ready:
				// already closed
			default:
				close(c.ready)
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

func (c *Client) joinChannel(name string) {
	if c.client == nil {
		return
	}

	// Search for existing channel
	ch := findChannelByName(c.client.Channels, name)
	if ch == nil {
		// Find root channel to create child
		root := findRootChannel(c.client.Channels)
		if root == nil {
			log.Printf("Failed to find root channel")
			return
		}
		// Create it
		root.Add(name, false)
		// Wait briefly for the channel to be created
		time.Sleep(500 * time.Millisecond)
		ch = findChannelByName(c.client.Channels, name)
		if ch == nil {
			log.Printf("Failed to create/find channel: %s", name)
			return
		}
		log.Printf("Created channel: %s", name)
	}

	// Move self to channel
	c.client.Self.Move(ch)
	log.Printf("Joined channel: %s", name)
}

// findChannelByName searches all channels for one with the given name.
func findChannelByName(channels gumble.Channels, name string) *gumble.Channel {
	for _, ch := range channels {
		if ch.Name == name {
			return ch
		}
	}
	return nil
}

// findRootChannel returns the server's root channel (no parent).
func findRootChannel(channels gumble.Channels) *gumble.Channel {
	for _, ch := range channels {
		if ch.IsRoot() {
			return ch
		}
	}
	return nil
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

// SendAudio sends PCM audio samples to the Mumble server.
// The gumble library handles Opus encoding internally.
// OpenAudio() must be called first.
func (c *Client) SendAudio(samples []int16) {
	c.mu.Lock()
	ch := c.audioCh
	c.mu.Unlock()

	if ch == nil {
		log.Printf("SendAudio: audioCh is nil!")
		return
	}
	select {
	case ch <- samples:
	default:
		log.Printf("SendAudio: channel full, dropping frame")
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
