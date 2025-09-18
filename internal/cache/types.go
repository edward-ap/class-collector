// Package cache defines the core data types used by snapshotting and delta
// computation in the bundle collector.
package cache

// SnapFile represents a single file entry in a snapshot.
// Path is a repo-relative path, Hash is a lowercase hex content hash (e.g., sha256),
// and Lines is the total line count (1-based, counting '\n').
type SnapFile struct {
	Path  string `json:"path"`
	Hash  string `json:"hash"`
	Lines int    `json:"lines"`
}

// Snapshot captures the state of a project at a specific moment.
// Module is a human-friendly identifier (e.g., artifactId or module name).
// Created is an ISO-8601 timestamp (UTC). PrevSrcDir is optional metadata
// that can help readers locate an earlier workspace. FormatVersion is a
// simple string to version the snapshot schema over time.
type Snapshot struct {
	Module        string     `json:"module"`
	Created       string     `json:"created"`
	PrevSrcDir    string     `json:"prevSrcDir,omitempty"`
	FormatVersion string     `json:"formatVersion,omitempty"`
	Files         []SnapFile `json:"files"`
}

// Delta describes the minimal set of changes from a previous snapshot to the
// current snapshot. The sets are mutually consistent after rename de-duplication:
//
//   - Added: files present now that were not in the previous snapshot
//   - Removed: files present previously that are no longer in the current snapshot
//   - Changed: files whose path is the same but content hash differs
//   - Renamed: files moved from one path to another without content change
//
// Notes:
//   - Renamed entries are one-to-one pairings (From → To) for the same content hash.
//   - Changed entries carry DiffPath (location inside a delta zip) and Oversize flag
//     indicating whether the textual diff was omitted due to size limits.
type Delta struct {
	Added   []SnapFile `json:"added"`
	Removed []SnapFile `json:"removed"`
	Renamed []struct {
		From string `json:"from"`
		To   string `json:"to"`
		Hash string `json:"hash"`
	} `json:"renamed"`
	Changed []struct {
		Path       string `json:"path"`
		HashBefore string `json:"hashBefore"`
		HashAfter  string `json:"hashAfter"`
		DiffPath   string `json:"diff"`
		Oversize   bool   `json:"oversize"`
	} `json:"changed"`
}
