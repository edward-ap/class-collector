// Package bundle: delta diff generation utilities.
//
// This module produces a map[patchName]patchBody for all changed files from cache.Delta,
// using the current files (files) and a readOld(hashBefore) callback to obtain the
// previous content from cache/blobs. If the old version is unavailable, an "added-only"
// patch is generated.
//
// Highlights:
//   - Windows-safe patch filenames (sanitization + uniqueness).
//   - Determinism: names are constructed identically for identical input.
//   - Diff size limit is controlled by diff.Options (see internal/diff).
//
// Note: the order of writing patches into the ZIP must be sorted at the archive-writing stage.
// We return a map here; determinism is ensured by sorting in the writer.
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"class-collector/internal/cache"
	"class-collector/internal/diff"
	"class-collector/internal/walkwalk"
)

// invalidFileCharsRe contains characters that are invalid in Windows filenames.
var invalidFileCharsRe = regexp.MustCompile(`[\\:*?"<>|]`)

// safeDiffBase returns a filesystem-safe base name for a patch (without .patch extension):
// it replaces slashes with '_' and removes invalid characters.
func safeDiffBase(p string) string {
	base := filepath.ToSlash(p)
	base = strings.ReplaceAll(base, "/", "_")
	base = invalidFileCharsRe.ReplaceAllString(base, "_")
	// Also trim leading dots/underscores to avoid odd names.
	base = strings.TrimLeft(base, "._")
	if base == "" {
		base = "patch"
	}
	return base
}

// shortHash returns the first 8 hex characters of the SHA-256 hash of s.
// Used as a stable suffix to avoid filename collisions.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// uniquePatchName constructs a unique patch filename considering names already used.
// If base+".patch" is taken, it appends a suffix using hashHint (or a hash of the base)
// until a free name is found. Returns only the filename (no directories).
func uniquePatchName(base, hashHint string, used map[string]struct{}) string {
	name := base + ".patch"
	if _, ok := used[name]; !ok {
		used[name] = struct{}{}
		return name
	}
	suffix := hashHint
	if suffix == "" {
		suffix = shortHash(base)
	}
	// First attempt: base-<suffix>.patch
	name = base + "-" + suffix + ".patch"
	if _, ok := used[name]; !ok {
		used[name] = struct{}{}
		return name
	}
	// In the very rare case of another collision — add one more short fingerprint.
	name = base + "-" + suffix + "-" + shortHash(base+suffix) + ".patch"
	used[name] = struct{}{}
	return name
}

// MakeDiffs generates patches for d.Changed.
//   - files: current files (to read the "b" content).
//   - opt: options like size limits (see internal/diff.Options).
//   - readOld: function to obtain the "a" content by old hash (may be nil).
//
// Returns map[patch_name]patch_text. Fields d.Changed[i].Oversize and .DiffPath
// are filled during generation.
func MakeDiffs(
	d cache.Delta,
	files []walkwalk.FileInfo,
	opt diff.Options,
	readOld func(hash string) ([]byte, error),
) (map[string]string, error) {

	// Index current files by relative path.
	byPath := make(map[string]walkwalk.FileInfo, len(files))
	for _, f := range files {
		byPath[f.RelPath] = f
	}

	out := make(map[string]string, len(d.Changed))
	usedNames := make(map[string]struct{}, len(d.Changed)) // to ensure unique names

	for i := range d.Changed {
		chg := &d.Changed[i]

		// Read old version (a).
		var a []byte
		if readOld != nil && chg.HashBefore != "" {
			if ab, err := readOld(chg.HashBefore); err == nil && len(ab) > 0 {
				a = ab
			}
		}

		// Read new version (b).
		var b []byte
		if fi, ok := byPath[chg.Path]; ok {
			if nb, err := os.ReadFile(fi.AbsPath); err == nil {
				b = nb
			}
		}

		// Build a deterministic, filesystem-safe patch name.
		base := safeDiffBase(chg.Path)
		// Prefer HashAfter as hashHint (when available) so names remain stable
		// for identical resulting content.
		hashHint := chg.HashAfter
		if hashHint == "" {
			// fallback: short hash of the path
			hashHint = shortHash(chg.Path)
		}
		patchName := uniquePatchName(base, hashHint[:min(len(hashHint), 8)], usedNames)

		// Build patch body.
		var body string
		var oversize bool
		if len(a) == 0 {
			body, oversize = diff.Added("b/"+chg.Path, b, opt)
		} else {
			body, oversize = diff.Unified("a/"+chg.Path, "b/"+chg.Path, a, b, opt)
			// Fallback if patch text looks suspiciously short or has no hunks
			if tooShortOrNoHunks(body) {
				body, oversize = diff.Added("b/"+chg.Path, b, opt)
			}
		}
		out[patchName] = body

		// Fill Delta.Change fields
		chg.Oversize = oversize
		chg.DiffPath = filepath.ToSlash(filepath.Join("diffs", patchName))
	}

	// Further ordering determinism is applied when writing ZIP (sort keys before writing).
	return out, nil
}

// min is a tiny helper to avoid importing math for integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// tooShortOrNoHunks heuristically detects suspicious patch bodies that likely
// lost context or were cleaned up too aggressively by the underlying library.
// We require at least one @@ hunk header and a minimal length.
func tooShortOrNoHunks(body string) bool {
	if len(body) < 32 { // tiny body is suspicious for non-empty changes
		return true
	}
	return !strings.Contains(body, "@@")
}
