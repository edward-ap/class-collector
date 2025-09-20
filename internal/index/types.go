// Package index defines the core data types used by source indexing,
// manifest assembly, slice generation and jump pointers for the bundle.
package index

// Anchor marks a named region in a source file. Line numbers are 1-based
// and inclusive on both ends.
type Anchor struct {
	Name  string `json:"name"`
	Start int    `json:"start"` // 1-based, inclusive
	End   int    `json:"end"`   // 1-based, inclusive
}

// ManFile describes a single source file in the manifest, including basic
// code intelligence (exports, anchors) and integrity metadata (hash, lines).
type ManFile struct {
	Path      string   `json:"path"`                // project-relative path with '/'
	Package   string   `json:"package,omitempty"`   // language package/namespace (if any)
	Class     string   `json:"class,omitempty"`     // primary type (e.g., Java class name)
	Kind      string   `json:"kind,omitempty"`      // "class"|"interface"|"enum"|"file"|...
	Summary   string   `json:"summary,omitempty"`   // optional short description
	Hash      string   `json:"hash,omitempty"`      // content hash (e.g., sha256 hex)
	Exports   []string `json:"exports,omitempty"`   // quick API surface (e.g., ["start()", ...])
	DependsOn []string `json:"dependsOn,omitempty"` // optional dependency hints
	Tags      []string `json:"tags,omitempty"`      // arbitrary labels (navigation)
	Lines     int      `json:"lines,omitempty"`     // total number of lines in file
	Anchors   []Anchor `json:"anchors,omitempty"`   // region anchors detected in file
}

// Manifest is the top-level index of a bundle/module.
type Manifest struct {
	Module       string    `json:"module"`                 // human-readable module name
	JDK          string    `json:"jdk,omitempty"`          // optional JDK version for Java projects
	Build        string    `json:"build,omitempty"`        // "maven"|"gradle"|"go"|"node"|...
	PackagesRoot string    `json:"packagesRoot,omitempty"` // optional packages root (if relevant)
	Entrypoints  []string  `json:"entrypoints,omitempty"`  // optional fully-qualified entry symbols
	SourceGlobs  []string  `json:"sourceGlobs,omitempty"`  // optional source patterns
	Files        []ManFile `json:"files"`                  // manifest entries (deterministic order)
	BundleID     string    `json:"bundle_id,omitempty"`    // canonical bundle hash (SHA-256 over sorted "path:hash\n")
}

// Symbol represents a discovered code symbol suitable for navigation.
// Start/End are 1-based line numbers within Path. End is finalized by the
// caller (usually set to next symbol start - 1, or file end).
type Symbol struct {
	Symbol string `json:"symbol"` // fully-qualified, e.g., "org.acme.Server.start"
	Kind   string `json:"kind"`   // "method"|"func"|"ctor"|...
	Path   string `json:"path"`   // project-relative file path
	Start  int    `json:"start"`  // 1-based
	End    int    `json:"end"`    // 1-based
}

// Symbols wraps the flat list for easier JSON emission/versioning.
type Symbols struct {
	Version int      `json:"version"` // schema/version stamp for future-proofing
	Symbols []Symbol `json:"symbols"`
}

// Slice is a coarse range within a file used for previews/jumps. When
// derived from anchors, Slice is anchor name; when chunked, "chunk_<start>".
type Slice struct {
	Path    string `json:"path"`
	Slice   string `json:"slice"`             // human-readable slice id
	Start   int    `json:"start"`             // 1-based, inclusive
	End     int    `json:"end"`               // 1-based, inclusive
	Summary string `json:"summary,omitempty"` // optional short description
}

// Pointer is a jump target. For symbol-backed pointers, Sym is set to the
// fully-qualified symbol; for anchor-backed pointers, Sym is empty and ID
// encodes file + anchor (with a stable slug).
type Pointer struct {
	ID    string `json:"id"`            // stable, unique within bundle
	Path  string `json:"path"`          // file path for the jump
	Sym   string `json:"sym,omitempty"` // fully-qualified symbol (if any)
	Start int    `json:"start"`         // 1-based, inclusive
	End   int    `json:"end"`           // 1-based, inclusive
}
