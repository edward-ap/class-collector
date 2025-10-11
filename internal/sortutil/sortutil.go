package sortutil

import "sort"

// StablePathSort returns a new slice containing the input paths sorted
// lexicographically. The original slice is not modified.
func StablePathSort(paths []string) []string {
	out := make([]string, len(paths))
	copy(out, paths)
	sort.Strings(out)
	return out
}
