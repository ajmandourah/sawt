// Package command parses and dispatches text commands from Mumble chat.
package command

import (
	"log"
	"regexp"
	"strings"

	"layeh.com/gumble/gumble"
)

// Action represents a parsed command and its arguments.
type Action struct {
	Command string
	Args    string
}

// Handler processes parsed commands and produces a response message.
// Return an empty string to send no response.
type Handler func(source *gumble.User, action *Action) string

// Dispatcher maps command names to handler functions.
type Dispatcher struct {
	prefix   string
	handlers map[string]Handler
}

// New creates a Dispatcher with the given command prefix.
func New(prefix string) *Dispatcher {
	return &Dispatcher{
		prefix:   prefix,
		handlers: make(map[string]Handler),
	}
}

// Register adds a command handler.
func (d *Dispatcher) Register(cmd string, h Handler) {
	d.handlers[strings.ToLower(cmd)] = h
}

// Parse checks if the message is a command and returns the parsed action.
// Returns nil if the message is not a command.
func (d *Dispatcher) Parse(message string) *Action {
	msg := strings.TrimSpace(message)
	if !strings.HasPrefix(msg, d.prefix) {
		return nil
	}

	rest := strings.TrimSpace(msg[len(d.prefix):])
	if rest == "" {
		return nil
	}

	parts := strings.SplitN(rest, " ", 2)
	cmd := strings.ToLower(parts[0])
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}

	// Mumble auto-links URLs with <a href="...">...</a> tags.
	// Strip all HTML so resolvers get clean input.
	args = stripHTML(args)

	return &Action{
		Command: cmd,
		Args:    args,
	}
}

// Dispatch executes the handler for the given action and returns the response.
func (d *Dispatcher) Dispatch(source *gumble.User, action *Action) string {
	h, ok := d.handlers[action.Command]
	if !ok {
		return ""
	}
	log.Printf("Command %q from %s: %s", action.Command, source.Name, action.Args)
	return h(source, action)
}

// stripHTML removes all HTML tags from a string and decodes HTML entities.
// Mumble auto-wraps URLs in <a href="...">...</a> tags and escapes entities.
var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

func stripHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, "")
	// Decode common HTML entities that Mumble inserts.
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	return s
}

// ListCommands returns all registered command names.
func (d *Dispatcher) ListCommands() []string {
	cmds := make([]string, 0, len(d.handlers))
	for cmd := range d.handlers {
		cmds = append(cmds, cmd)
	}
	return cmds
}
