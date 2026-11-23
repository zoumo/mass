package testpeer

import (
	"maps"
	"slices"
)

// RuntimeEnv builds literal entries for a Runtime config JSON array.
func RuntimeEnv(values map[string]string) []any {
	entries := make([]any, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		entries = append(entries, map[string]any{"name": name, "value": values[name]})
	}
	return entries
}
