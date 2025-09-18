// Package validate performs lightweight, dependency-free validation of the
// bundle artifacts. It is not a full JSON-Schema validator; instead it checks
// structural and semantic constraints that commonly catch bad bundles.
//
// Goals:
//   - No external dependencies (stdlib only)
//   - Aggregate multiple issues into a single error for better UX
//   - Deterministic, strict-enough checks without being overbearing
package validate

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"class-collector/internal/index"
)

// Manifest validates high-level constraints on the assembled manifest:
//
//   - Module should be non-empty.
//   - Each file must have a normalized relative path (no absolute, no "..").
//   - Hash, if present, must be a 64-char lowercase hex (sha256).
//   - Lines >= 1.
//   - Anchors must have non-empty names, 1-based ranges, Start <= End,
//     and End <= file Lines.
//   - No duplicate file paths.
//   - Optional: warn-as-error on backslashes in paths (ZIP uses forward slashes).
//
// The function returns nil if everything looks fine, or a single aggregated
// error describing all the issues found.
func Manifest(m index.Manifest) error {
	var errs errlist

	if strings.TrimSpace(m.Module) == "" {
		errs.add("manifest.module must be non-empty")
	}

	seen := make(map[string]struct{}, len(m.Files))
	for i, f := range m.Files {
		prefix := fmt.Sprintf("files[%d] (%s)", i, f.Path)

		// Path checks
		if f.Path == "" {
			errs.add("%s: path must be non-empty", prefix)
		} else {
			if filepath.IsAbs(f.Path) {
				errs.add("%s: path must be relative, got absolute %q", prefix, f.Path)
			}
			if strings.HasPrefix(f.Path, "/") || strings.HasPrefix(f.Path, "\\") {
				errs.add("%s: path must not start with a slash (got %q)", prefix, f.Path)
			}
			if strings.Contains(f.Path, `\`) {
				errs.add("%s: path must use forward slashes ('/'), found backslash", prefix)
			}
			if hasDotDot(f.Path) {
				errs.add("%s: path must not contain '..' segments (got %q)", prefix, f.Path)
			}
		}

		// Duplicate path detection
		if _, dup := seen[f.Path]; dup {
			errs.add("%s: duplicate file path %q", prefix, f.Path)
		} else if f.Path != "" {
			seen[f.Path] = struct{}{}
		}

		// Hash (sha256 hex — 64 lowercase hex chars)
		if f.Hash != "" && !reHex64.MatchString(f.Hash) {
			errs.add("%s: hash must be 64 lowercase hex chars (sha256), got %q", prefix, f.Hash)
		}

		// Lines
		if f.Lines < 1 {
			errs.add("%s: lines must be >= 1 (got %d)", prefix, f.Lines)
		}

		// Anchors
		for j, a := range f.Anchors {
			ap := fmt.Sprintf("%s.anchors[%d] (%s)", prefix, j, a.Name)
			if strings.TrimSpace(a.Name) == "" {
				errs.add("%s: name must be non-empty", ap)
			}
			if a.Start < 1 {
				errs.add("%s: start must be >= 1 (got %d)", ap, a.Start)
			}
			if a.End < a.Start {
				errs.add("%s: end must be >= start (start=%d, end=%d)", ap, a.Start, a.End)
			}
			if f.Lines > 0 && a.End > f.Lines {
				errs.add("%s: end must be <= file lines (%d), got %d", ap, f.Lines, a.End)
			}
		}
	}

	// Optional determinism check: ensure manifest files are sorted by path.
	// (Harmless if not, but helps keep ZIP byte-for-byte stable.)
	if !isSortedByPath(m.Files) {
		errs.add("manifest.files should be sorted by path for deterministic bundles")
	}

	return errs.err()
}

// Symbols validates the flat symbols list:
//
//   - Version >= 1
//   - Every symbol has non-empty Symbol and Path
//   - Start >= 1, End >= Start
//   - (Optional) deterministic order (by Path, Start, End) — warned as error
func Symbols(s index.Symbols) error {
	var errs errlist

	if s.Version < 1 {
		errs.add("symbols.version must be >= 1 (got %d)", s.Version)
	}

	for i, sym := range s.Symbols {
		prefix := fmt.Sprintf("symbols[%d] (%s)", i, sym.Symbol)
		if strings.TrimSpace(sym.Symbol) == "" {
			errs.add("%s: symbol must be non-empty", prefix)
		}
		if strings.TrimSpace(sym.Path) == "" {
			errs.add("%s: path must be non-empty", prefix)
		} else {
			if filepath.IsAbs(sym.Path) {
				errs.add("%s: path must be relative, got absolute %q", prefix, sym.Path)
			}
			if strings.Contains(sym.Path, `\`) {
				errs.add("%s: path must use forward slashes ('/'), found backslash", prefix)
			}
			if hasDotDot(sym.Path) {
				errs.add("%s: path must not contain '..' segments", prefix)
			}
		}
		if sym.Start < 1 {
			errs.add("%s: start must be >= 1 (got %d)", prefix, sym.Start)
		}
		if sym.End < sym.Start {
			errs.add("%s: end must be >= start (start=%d, end=%d)", prefix, sym.Start, sym.End)
		}
	}

	// Optional determinism check: encourage sorted output.
	if !isSortedSymbols(s.Symbols) {
		errs.add("symbols list should be sorted (path, start, end) for determinism")
	}

	return errs.err()
}

// --- helpers -----------------------------------------------------------------

var reHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func hasDotDot(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func isSortedByPath(files []index.ManFile) bool {
	if len(files) < 2 {
		return true
	}
	cp := make([]string, len(files))
	for i := range files {
		cp[i] = files[i].Path
	}
	return sort.SliceIsSorted(cp, func(i, j int) bool { return cp[i] < cp[j] })
}

func isSortedSymbols(syms []index.Symbol) bool {
	if len(syms) < 2 {
		return true
	}
	return sort.SliceIsSorted(syms, func(i, j int) bool {
		if syms[i].Path == syms[j].Path {
			if syms[i].Start == syms[j].Start {
				return syms[i].End < syms[j].End
			}
			return syms[i].Start < syms[j].Start
		}
		return syms[i].Path < syms[j].Path
	})
}

// errlist aggregates multiple validation issues into a single error.
type errlist struct {
	msgs []string
}

func (e *errlist) add(format string, args ...any) {
	if e == nil {
		return
	}
	e.msgs = append(e.msgs, fmt.Sprintf(format, args...))
}

func (e *errlist) err() error {
	if e == nil || len(e.msgs) == 0 {
		return nil
	}
	// Join with newline for readability.
	return errors.New(strings.Join(e.msgs, "\n"))
}
