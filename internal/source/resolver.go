// Package source resolves user input into playable audio sources.
// Resolvers are tried in order; the first match wins.
package source

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// SourceType indicates how a track was resolved.
type SourceType string

const (
	SourceLocal  SourceType = "local"
	SourceDirect SourceType = "direct"
	SourceYtDlp  SourceType = "ytdlp"
)

// ResolvedSource is the output of a successful resolution.
type ResolvedSource struct {
	URL   string // Final playable URL or file path for FFmpeg
	Title string // Human-readable title
	Type  SourceType
}

// Resolver turns user input into a ResolvedSource.
type Resolver interface {
	CanHandle(input string) bool
	Resolve(ctx context.Context, input string) (*ResolvedSource, error)
}

// ResolveLogger is called during resolution to report progress.
// Use this to log retry attempts to Mumble chat and stderr.
type ResolveLogger func(msg string)

// Chain holds an ordered list of resolvers.
type Chain struct {
	resolvers  []Resolver
	logger     ResolveLogger // optional callback for Mumble/log notifications
	maxRetries int           // max retries per resolver (0 = no retry)
	retryDelay time.Duration // base delay between retries
}

// NewChain creates a Chain with the given resolvers, tried in order.
// Optionally pass a logger for progress notifications (e.g., Mumble chat).
func NewChain(logger ResolveLogger, resolvers ...Resolver) *Chain {
	return &Chain{
		resolvers:  resolvers,
		logger:     logger,
		maxRetries: 3,
		retryDelay: 2 * time.Second,
	}
}

// Resolve tries each resolver in order until one succeeds.
// If a resolver's Resolve() returns an error, the chain falls through
// to the next resolver. Returns an error only if no resolver could handle.
// Retries failed resolvers up to maxRetries times with increasing delays.
func (c *Chain) Resolve(ctx context.Context, input string) (*ResolvedSource, error) {
	var lastErr error
	for _, r := range c.resolvers {
		if !r.CanHandle(input) {
			continue
		}

		if c.logger != nil {
			c.logger(fmt.Sprintf("⏳ Resolving %s with %s...", truncate(input, 40), resolverName(r)))
		}

		src, err := c.resolveWithRetry(ctx, r, input)
		if err != nil {
			lastErr = err
			if c.logger != nil {
				c.logger(fmt.Sprintf("❌ %s failed: %v", resolverName(r), err))
			}
			continue // try next resolver
		}

		if c.logger != nil {
			c.logger(fmt.Sprintf("✅ Resolved: %s", src.Title))
		}
		return src, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no resolver could handle input")
}

// resolveWithRetry attempts resolution with exponential backoff retries.
func (c *Chain) resolveWithRetry(ctx context.Context, r Resolver, input string) (*ResolvedSource, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			delay := c.retryDelay * time.Duration(1<<uint(attempt-1)) // 2s, 4s, 8s
			log.Printf("[resolver] Retry %d/%d for %s (wait %v)", attempt, c.maxRetries, resolverName(r), delay)
			if c.logger != nil {
				c.logger(fmt.Sprintf("🔄 Retry %d/%d for %s (%v wait)...", attempt, c.maxRetries, resolverName(r), delay))
			}

			// Check context before waiting
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		src, err := r.Resolve(ctx, input)
		if err == nil {
			if attempt > 0 {
				log.Printf("[resolver] Resolution succeeded on attempt %d", attempt+1)
			}
			return src, nil
		}
		log.Printf("[resolver] Attempt %d failed for %s: %v", attempt+1, resolverName(r), err)
		lastErr = err
	}
	return nil, lastErr
}

// ResolveFiles resolves input into multiple tracks (e.g. directory scanning).
// Returns a single ResolvedSource for single-file resolvers.
func (c *Chain) ResolveFiles(ctx context.Context, input string) ([]*ResolvedSource, error) {
	src, err := c.Resolve(ctx, input)
	if err != nil {
		return nil, err
	}
	return []*ResolvedSource{src}, nil
}

// resolverName returns a human-readable name for a resolver.
func resolverName(r Resolver) string {
	switch r.(type) {
	case *YtDlpResolver:
		return "yt-dlp"
	case *LocalResolver:
		return "local"
	case *DirectResolver:
		return "direct"
	default:
		return fmt.Sprintf("%T", r)
	}
}

// truncate shortens a string to maxLen chars, adding "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// compile-time checks
var _ = strings.TrimSpace
