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
	"sort"
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
	byPath := make(map[string]walkwalk.FileInfo, len(files))
	for _, f := range files {
		byPath[f.RelPath] = f
	}

	patches := make([]generatedPatch, 0, len(d.Changed))
	usedNames := make(map[string]struct{}, len(d.Changed))

	for i := range d.Changed {
		chg := &d.Changed[i]

		var oldData []byte
		if readOld != nil && chg.HashBefore != "" {
			if data, err := readOld(chg.HashBefore); err == nil && len(data) > 0 {
				oldData = data
			}
		}

		var newData []byte
		if fi, ok := byPath[chg.Path]; ok {
			if data, err := os.ReadFile(fi.AbsPath); err == nil {
				newData = data
			}
		}

		base := safeDiffBase(chg.Path)
		hashHint := chg.HashAfter
		if hashHint == "" {
			hashHint = shortHash(chg.Path)
		}
		patchName := uniquePatchName(base, hashHint[:min(len(hashHint), 8)], usedNames)
		body, oversize := diffFile(chg.Path, opt, oldData, newData)

		patches = append(patches, generatedPatch{name: patchName, body: body, oversize: oversize})

		summary := summarizePatch(patchName, oversize)
		chg.Oversize = summary.oversize
		chg.DiffPath = summary.diffPath
	}

	sorted := sortAndPackage(patches)
	out := make(map[string]string, len(sorted))
	for _, p := range sorted {
		out[p.name] = p.body
	}
	return out, nil
}

type generatedPatch struct {
	name     string
	body     string
	oversize bool
}

type patchSummary struct {
	diffPath string
	oversize bool
}

func diffFile(path string, opt diff.Options, oldData, newData []byte) (string, bool) {
	aName := "a/" + path
	bName := "b/" + path
	if opt.NoPrefix {
		aName = path
		bName = path
	}
	if len(oldData) == 0 {
		return diff.Added(bName, newData, opt)
	}
	body, oversize := diff.Unified(aName, bName, oldData, newData, opt)
	if tooShortOrNoHunks(body) {
		return diff.Added(bName, newData, opt)
	}
	return body, oversize
}

func summarizePatch(patchName string, oversize bool) patchSummary {
	diffPath := filepath.ToSlash(filepath.Join("diffs", patchName))
	return patchSummary{diffPath: diffPath, oversize: oversize}
}

func sortAndPackage(patches []generatedPatch) []generatedPatch {
	if len(patches) <= 1 {
		return patches
	}
	sort.Slice(patches, func(i, j int) bool { return patches[i].name < patches[j].name })
	return patches
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
