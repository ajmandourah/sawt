// Package source resolves user input into playable audio sources.
// Resolvers are tried in order; the first match wins.
package source

import (
	"context"
	"fmt"
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

// Chain holds an ordered list of resolvers.
type Chain struct {
	resolvers []Resolver
}

// NewChain creates a Chain with the given resolvers, tried in order.
func NewChain(resolvers ...Resolver) *Chain {
	return &Chain{resolvers: resolvers}
}

// Resolve tries each resolver in order until one succeeds.
// Returns ErrNoResolver if no resolver could handle the input.
func (c *Chain) Resolve(ctx context.Context, input string) (*ResolvedSource, error) {
	for _, r := range c.resolvers {
		if !r.CanHandle(input) {
			continue
		}
		src, err := r.Resolve(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("resolve %T: %w", r, err)
		}
		return src, nil
	}
	return nil, fmt.Errorf("no resolver could handle input")
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
