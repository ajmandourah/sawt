package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// audioExts maps known audio extensions to true for quick lookup.
var audioExts = map[string]bool{
	".mp3": true, ".wav": true, ".flac": true, ".ogg": true,
	".m4a": true, ".wma": true, ".aac": true, ".opus": true,
	".aiff": true, ".aif": true, ".alac": true,
}

// LocalResolver handles local files and directories.
type LocalResolver struct{}

func (r *LocalResolver) CanHandle(input string) bool {
	// Skip URLs — they're handled by DirectResolver.
	if isURL(input) {
		return false
	}
	abs, err := filepath.Abs(input)
	if err != nil {
		return false
	}
	info, err := os.Stat(abs)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return true
	}
	return audioExts[strings.ToLower(filepath.Ext(abs))]
}

func (r *LocalResolver) Resolve(ctx context.Context, input string) (*ResolvedSource, error) {
	abs, err := filepath.Abs(input)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("cannot access %s: %w", abs, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory; use ResolveDir instead", abs)
	}
	return &ResolvedSource{
		URL:   abs,
		Title: filepath.Base(abs),
		Type:  SourceLocal,
	}, nil
}

// ResolveDir scans a directory for audio files and returns them sorted.
func (r *LocalResolver) ResolveDir(ctx context.Context, dirPath string) ([]*ResolvedSource, error) {
	abs, err := filepath.Abs(dirPath)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("cannot access %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", abs)
	}

	var results []*ResolvedSource
	err = filepath.Walk(abs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if audioExts[strings.ToLower(filepath.Ext(path))] {
			results = append(results, &ResolvedSource{
				URL:   path,
				Title: filepath.Base(path),
				Type:  SourceLocal,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", abs, err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no audio files found in %s", abs)
	}

	// Sort for consistent ordering.
	sortResolved(results)
	return results, nil
}
