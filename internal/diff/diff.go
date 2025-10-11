// Package diff provides unified-diff generation utilities for changed files.
// It uses github.com/pmezard/go-difflib/difflib to produce classic unified
// patches (---/+++ headers, @@ hunks, lines prefixed with ' ', '-', '+').
package diff

import (
	"fmt"
	"strings"
	"time"

	difflib "github.com/pmezard/go-difflib/difflib"
)

// Options controls patch generation behavior.
type Options struct {
	// MaxBytes is a guardrail on input size (old+new). When exceeded,
	// a minimal placeholder patch is returned and oversize=true.
	// 0 means "no limit".
	MaxBytes int

	// TimeoutSeconds kept for backward compatibility. difflib does not use it.
	TimeoutSeconds float64

	// Context controls the number of CONTEXT LINES in unified hunks.
	// If 0, default to 4.
	Context int

	// NoPrefix controls whether FromFile/ToFile are prefixed with "a/" and "b/".
	// When true, the paths passed by the caller are used as-is.
	NoPrefix bool

	// LineMode kept for backward compatibility (unified output is line-based).
	LineMode bool
}

// Unified produces a classic unified patch for a↦b.
// Returns the patch body and a flag indicating it was omitted due to size.
func Unified(aName, bName string, a, b []byte, opt Options) (body string, oversize bool) {
	// Size guardrail.
	if opt.MaxBytes > 0 && (len(a)+len(b)) > opt.MaxBytes {
		return omitted(aName, bName), true
	}

	ctx := opt.Context
	if ctx <= 0 {
		ctx = 4
	}

	ua := splitLinesKeepNL(string(a))
	ub := splitLinesKeepNL(string(b))

	u := difflib.UnifiedDiff{
		A:        ua,
		B:        ub,
		FromFile: aName,
		ToFile:   bName,
		Context:  ctx,
	}
	s, err := difflib.GetUnifiedDiffString(u)
	if err != nil || s == "" {
		// Very rare; return placeholder instead of an empty patch.
		return omitted(aName, bName), false
	}
	return s, false
}

// Added produces a patch that adds the entire content b (no old version).
func Added(bName string, b []byte, opt Options) (string, bool) {
	if opt.MaxBytes > 0 && len(b) > opt.MaxBytes {
		return omitted("/dev/null", bName), true
	}
	ctx := opt.Context
	if ctx <= 0 {
		ctx = 4
	}
	// Ensure no "b/" prefix in ToFile per policy.
	if strings.HasPrefix(bName, "b/") {
		bName = bName[2:]
	}
	u := difflib.UnifiedDiff{
		A:        []string{},                  // empty "from"
		B:        splitLinesKeepNL(string(b)), // new content
		FromFile: "/dev/null",
		ToFile:   bName,
		Context:  ctx,
	}
	s, err := difflib.GetUnifiedDiffString(u)
	if err != nil || s == "" {
		return omitted("/dev/null", bName), false
	}
	return s, false
}

// splitLinesKeepNL splits into lines and keeps newline characters,
// which produces better unified hunks.
func splitLinesKeepNL(s string) []string {
	if s == "" {
		return []string{}
	}
	// SplitAfter keeps the "\n" at the end of each element.
	lines := strings.SplitAfter(s, "\n")
	// If file does not end with a newline, SplitAfter keeps the last chunk
	// without "\n" — this is fine for unified output.
	return lines
}

// header kept for compatibility with earlier code paths (not used by difflib).
func header(aName, bName string) string {
	return fmt.Sprintf("--- %s\n+++ %s\n", aName, bName)
}

// omitted returns a compact placeholder when size limits are exceeded.
func omitted(aName, bName string) string {
	_ = time.Second // keep import stability if Options uses TimeoutSeconds elsewhere
	return fmt.Sprintf("--- %s\n+++ %s\n@@\n# diff omitted (oversize)\n", aName, bName)
}
