package source

import "sort"

// isURL returns true if the input looks like an HTTP(S) URL.
func isURL(input string) bool {
	return len(input) >= 7 && (input[:7] == "http://") || (len(input) >= 8 && input[:8] == "https://")
}

// sortResolved sorts resolved sources by title for consistent ordering.
func sortResolved(sources []*ResolvedSource) {
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].Title < sources[j].Title
	})
}
