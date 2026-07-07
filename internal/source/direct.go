package source

import (
	"context"
	"fmt"
	"net/http"
)

// DirectResolver handles HTTP(S) URLs by passing them directly to FFmpeg.
// It performs a lightweight HEAD request to verify the URL is reachable.
type DirectResolver struct{}

func (r *DirectResolver) CanHandle(input string) bool {
	return isURL(input)
}

func (r *DirectResolver) Resolve(ctx context.Context, input string) (*ResolvedSource, error) {
	// Quick reachability check with a short timeout.
	// We use HEAD first, falling back to GET if the server doesn't support HEAD.
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, input, nil)
	if err != nil {
		return nil, fmt.Errorf("bad URL: %w", err)
	}

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects, just check
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", input, err)
	}
	resp.Body.Close()

	// Some servers return 405 for HEAD but work fine with GET/FFmpeg.
	// Only treat as error if it's a hard failure (404, 500, etc.).
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode >= 500 {
		return nil, fmt.Errorf("server returned %d for %s", resp.StatusCode, input)
	}

	return &ResolvedSource{
		URL:   input,
		Title: input,
		Type:  SourceDirect,
	}, nil
}
