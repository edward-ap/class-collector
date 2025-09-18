// Package main provides the class-collector CLI that scans a source tree and
// produces either a FULL bundle (-zip), a DELTA bundle (-delta), or a CHAT
// bundle (-chat). It focuses on deterministic, reproducible outputs and a safe
// cache/delta workflow.
//
// Modes:
//   - FULL bundle  : class-collector <src_dir> -zip out.zip [flags]
//   - DELTA bundle : class-collector <src_dir> -delta out.delta.zip [flags]
//   - (Markdown mode is deprecated; use -zip or -delta.)
//
// Key design goals:
//   - Deterministic output (sorted entries, fixed ZIP timestamps handled by bundle)
//   - Safe cache/delta workflow (atomic snapshot writes, optional content-addressed blobs)
//   - Clear, minimal CLI flags with sensible defaults
package main

import (
	"bytes"
	"class-collector/internal/graph"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"class-collector/internal/bundle"
	"class-collector/internal/cache"
	"class-collector/internal/diff"
	"class-collector/internal/index"
	"class-collector/internal/meta"
	"class-collector/internal/validate"
	"class-collector/internal/walkwalk"
)

type dualFS struct{ oldRoot, newRoot string }

func (d dualFS) Read(p string, old bool) ([]byte, error) {
	root := d.newRoot
	if old {
		root = d.oldRoot
	}
	full := filepath.Join(root, filepath.FromSlash(p))
	return os.ReadFile(full)
}

// splitCSV converts a comma-separated list into a slice without trimming quotes.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, 8)
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			p := s[start:i]
			if p != "" {
				out = append(out, p)
			}
			start = i + 1
		}
	}
	return out
}

// toSet builds a string->struct{} set from a slice, skipping empty strings.
func toSet(list []string) map[string]struct{} {
	m := make(map[string]struct{}, len(list))
	for _, v := range list {
		if v != "" {
			m[v] = struct{}{}
		}
	}
	return m
}

func main() {
	// ----- Flags & usage ------------------------------------------------------
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  FULL  : %s -zip out.zip [flags] <src_dir>\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  DELTA : %s -delta out.delta.zip [flags] <src_dir>\n", filepath.Base(os.Args[0]))
		fmt.Fprintln(os.Stderr, "  (Use -- to separate flags from the positional path if needed.)")
		fmt.Fprintln(os.Stderr, "\nCommon flags:")
		flag.PrintDefaults()
	}

	// Selection & walking
	extsFlag := flag.String("ext", ".go,.java,.kt,.cs,.ts,.tsx,.js,.json,.yaml,.yml,.xml,.proto,.gradle,.md,.txt",
		"comma-separated extensions to include")
	// Exclude by base-name prefix (dirs/files). Example: .git,node_modules,target
	excludeFlag := flag.String("exclude", ".git,node_modules,dist,build,out,target,.idea,.vscode,.DS_Store",
		"comma-separated dir/file prefixes to exclude")
	includeFlag := flag.String("include", "",
		"comma-separated substrings to force-include (in path)")
	maxBytesFlag := flag.Int64("max-bytes", 25_000_000,
		"approx max total bytes to include in FULL mode (0 = no limit)")
	maxFileBytesFlag := flag.Int64("max-file-bytes", 2_000_000, "max bytes per file to include (0 = no limit)")
	useGitignoreFlag := flag.Bool("use-gitignore", true, "honor .gitignore patterns during file walk")
	followSymlinks := flag.Bool("follow-symlinks", false,
		"follow symlinks during walk")

	// Modes
	zipFlag := flag.String("zip", "", "path to output FULL zip bundle (mutually exclusive with -delta/-chat)")
	deltaFlag := flag.String("delta", "", "path to output DELTA zip bundle (mutually exclusive with -zip/-chat)")
	chatFlag := flag.String("chat", "", "path to output CHAT zip (mutually exclusive with -zip/-delta)")
	chatMaxClasses := flag.Int("chat-max-classes", 10, "max classes/entities per chat message")
	chatMaxChars := flag.Int("chat-max-chars", 80_000, "max characters per chat message")

 	// Cache & diffs
	tmpDirFlag := flag.String("tmp-dir", "tmp/.ccache", "base cache directory for snapshots and blobs")
	newFlag := flag.Bool("new", false, "reset cache for this <src_dir> before building")
	storeBlobsFlag := flag.Bool("store-blobs", false, "store source copies as content-addressed blobs for diffs")
	maxDiffBytesFlag := flag.Int("max-diff-bytes", 2_000_000, "max bytes for diffs in -delta (0 = no limit)")
	renameSimFlag := flag.Bool("rename-similarity", false, "enable similarity-based rename detection in -delta")
	renameSimThresh := flag.Int("rename-sim-thresh", 8, "max Hamming distance for SimHash to classify as rename")
	renameSimOldRoot := flag.String("rename-sim-oldroot", "", "old snapshot root for reading removed files (optional)")

	// Indexing & output
	emitSrcFlag := flag.Bool("emit-src", false, "include source copies in the FULL zip under src/")
	maxFileLinesFlag := flag.Int("max-file-lines", 500, "max lines per file before slicing; anchors preferred")
	langHintFlag := flag.String("lang", "", "limit symbol extraction to languages (comma list: java,go,ts,tsx,js)")
	validateFlag := flag.Bool("validate", true, "validate manifest/symbols JSON against schemas (if available)")
	saveSnapOnFull := flag.Bool("save-snapshot", true, "save snapshot in tmp after FULL (-zip)")
	// Auto-anchors
	autoAnchorsFlag := flag.Bool("auto-anchors", true, "synthesize virtual anchors from symbols/imports/tests")
	autoAnchorsMin := flag.Int("auto-anchors-min-lines", 8, "minimum region length for auto anchors")
	autoAnchorsMax := flag.Int("auto-anchors-max-per-file", 64, "maximum number of auto anchors per file (0 = unlimited)")
	autoAnchorsImports := flag.Bool("auto-anchors-imports", true, "add IMPORTS anchor if an import block exists")
	autoAnchorsTests := flag.Bool("auto-anchors-tests", true, "add test anchors (Go: Test*/Benchmark*/Example*, TS: describe/it/test)")
	autoAnchorsPrefix := flag.String("auto-anchors-prefix", "auto:", "prefix for auto anchor names")

	flag.Parse()

	zipMode := *zipFlag != ""
	deltaMode := *deltaFlag != ""
	chatMode := *chatFlag != ""
	if (zipMode && deltaMode) || (zipMode && chatMode) || (deltaMode && chatMode) {
		fmt.Fprintln(os.Stderr, "ERROR: -zip, -delta and -chat are mutually exclusive")
		os.Exit(2)
	}

	needMarkdown := (!zipMode && !deltaMode && !chatMode)
	if (needMarkdown && flag.NArg() != 2) || (!needMarkdown && flag.NArg() < 1) {
		flag.Usage()
		os.Exit(2)
	}
	srcDir := filepath.Clean(flag.Arg(0))
	var outFile string
	if needMarkdown {
		outFile = flag.Arg(1) // deprecated mode
	}
	if (zipMode || deltaMode || chatMode) && outFile != "" {
		fmt.Fprintln(os.Stderr, "Note: ignoring markdown output path because -zip/-delta/-chat was provided")
	}

	// ----- Cache directory & reset ------------------------------------------
	srcAbs, _ := filepath.Abs(srcDir)
	ccDir := cache.CacheDir(*tmpDirFlag, srcAbs)
	if *newFlag {
		_ = cache.Clear(ccDir)
	}

	// ----- Walk & collect files ---------------------------------------------
	exts := toSet(splitCSV(*extsFlag))
	exclude := toSet(splitCSV(*excludeFlag))
	includes := splitCSV(*includeFlag)
	langHints := toSet(splitCSV(*langHintFlag))

	maxBytes := *maxBytesFlag
	if deltaMode && maxBytes > 0 {
		// In delta mode we must see all candidates for accurate snapshot/delta.
		fmt.Fprintln(os.Stderr, "Note: ignoring -max-bytes in -delta mode")
		maxBytes = 0
	}

	files, _, err := walkwalk.CollectFiles(srcDir, exts, exclude, includes, maxBytes, *maxFileBytesFlag, *useGitignoreFlag, *followSymlinks)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "No files matched filters.")
		os.Exit(0)
	}

	// ----- Build current snapshot & optional blob store ----------------------
	curr := &cache.Snapshot{
		Module:        filepath.Base(srcDir),
		Created:       time.Now().UTC().Format(time.RFC3339),
		PrevSrcDir:    "",  // optional metadata (not required)
		FormatVersion: "1", // bump if snapshot schema changes
		Files:         make([]cache.SnapFile, 0, len(files)),
	}

	for _, f := range files {
		data, err := os.ReadFile(f.AbsPath)
		if err != nil {
			continue
		}
		lines := 1 + bytes.Count(data, []byte("\n"))
		curr.Files = append(curr.Files, cache.SnapFile{
			Path:  f.RelPath,
			Hash:  f.SHA256Hex,
			Lines: lines,
		})
		if *storeBlobsFlag && len(f.SHA256Hex) >= 6 {
			// Store a content-addressed blob for old-vs-new diffs.
			_ = cache.SaveBlob(ccDir, f.SHA256Hex, bytes.NewReader(data))
		}
 }

// =====================  DELTA MODE  =====================================
	if deltaMode {
		prev, _ := cache.Load(ccDir)
		if prev == nil {
			prev = &cache.Snapshot{Module: curr.Module}
		}
		// Configure rename similarity for this run (before computing delta).
		cache.SetRenameSimilarity(*renameSimFlag, *renameSimThresh)
		if *renameSimFlag && *renameSimOldRoot != "" {
			cache.SetContentProvider(dualFS{oldRoot: *renameSimOldRoot, newRoot: srcDir})
		}

		d := cache.BuildDelta(prev, curr)

		// Prepare a reader for "old" content from the blob store.
		readOld := func(hash string) ([]byte, error) {
			if len(hash) < 6 {
				return nil, fs.ErrNotExist
			}
			return cache.ReadBlob(ccDir, hash)
		}

		// Generate diffs for d.Changed. Map key → patch text.
		diffs, _ := bundle.MakeDiffs(d, files, diff.Options{
			MaxBytes:       *maxDiffBytesFlag,
			TimeoutSeconds: 5.0,
			PatchMargin:    16, // more context for stabler patches
			LineMode:       true,
		}, readOld)

		// Build a stable delta index payload.
		type Changed struct {
			Path       string `json:"path"`
			HashBefore string `json:"hashBefore"`
			HashAfter  string `json:"hashAfter"`
			Diff       string `json:"diff"`
			Oversize   bool   `json:"oversize,omitempty"`
			Truncated  bool   `json:"truncated,omitempty"` // mirrors Oversize; clearer name than "binary"
		}
		changed := make([]Changed, 0, len(d.Changed))
		overs := 0
		for _, ch := range d.Changed {
			if ch.Oversize {
				overs++
			}
			changed = append(changed, Changed{
				Path:       ch.Path,
				HashBefore: ch.HashBefore,
				HashAfter:  ch.HashAfter,
				Diff:       ch.DiffPath,
				Oversize:   ch.Oversize,
				Truncated:  ch.Oversize,
			})
		}

		// Ensure non-nil slices for JSON ([] instead of null)
		addedArr := append([]cache.SnapFile{}, d.Added...)
		removedArr := append([]cache.SnapFile{}, d.Removed...)
		renamedArr := append([]struct {
			From string `json:"from"`
			To   string `json:"to"`
			Hash string `json:"hash"`
		}{}, d.Renamed...)
		changedArr := append([]Changed{}, changed...)

		di := struct {
			BaseModule   string           `json:"baseModule"`
			BaseSnapshot string           `json:"baseSnapshot"`
			HeadSnapshot string           `json:"headSnapshot"`
			Added        []cache.SnapFile `json:"added"`
			Removed      []cache.SnapFile `json:"removed"`
			Renamed      []struct {
				From string `json:"from"`
				To   string `json:"to"`
				Hash string `json:"hash"`
			} `json:"renamed"`
			Changed []Changed `json:"changed"`
		}{
			BaseModule:   curr.Module,
			BaseSnapshot: prev.Created,
			HeadSnapshot: curr.Created,
			Added:        addedArr,
			Removed:      removedArr,
			Renamed:      renamedArr,
			Changed:      changedArr,
		}

		// Optional: include full payloads of newly added files (stable order).
		var addedFiles []struct{ RelPath, AbsPath string }
		if len(d.Added) > 0 {
			byRel := make(map[string]string, len(files))
			for _, f := range files {
				byRel[f.RelPath] = f.AbsPath
			}
			for _, a := range d.Added {
				if ap, ok := byRel[a.Path]; ok {
					addedFiles = append(addedFiles, struct{ RelPath, AbsPath string }{
						RelPath: a.Path, AbsPath: ap,
					})
				}
			}
			sort.Slice(addedFiles, func(i, j int) bool { return addedFiles[i].RelPath < addedFiles[j].RelPath })
		}

		if err := bundle.WriteDelta(*deltaFlag, di, diffs, addedFiles); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			os.Exit(1)
		}
		_ = cache.Save(ccDir, curr)

		fmt.Printf(
			"Wrote delta bundle %s (added=%d, removed=%d, changed=%d, renamed=%d, oversize=%d)\n",
			*deltaFlag, len(d.Added), len(d.Removed), len(d.Changed), len(d.Renamed), overs,
		)
		return
	}

	// ======================  CHAT MODE  ======================================
	if chatMode {
		// Build graph for potential ranking (currently deterministic path order)
		gf := make([]graph.File, 0, len(files))
		for _, f := range files {
			gf = append(gf, graph.File{RelPath: f.RelPath, AbsPath: f.AbsPath, Ext: f.Ext})
		}
		g := graph.BuildFrom(gf)

		// Auto-anchors config still influences manifest builds
		index.SetAutoAnchorsConfig(index.AutoAnchorConfig{
			Enabled:        *autoAnchorsFlag,
			MinLines:       *autoAnchorsMin,
			MaxPerFile:     *autoAnchorsMax,
			IncludeImports: *autoAnchorsImports,
			IncludeTests:   *autoAnchorsTests,
			Prefix:         *autoAnchorsPrefix,
		})

		man, syms, _, _ := index.BuildArtifacts(srcDir, files, *maxFileLinesFlag, langHints)

		// Emit only those source files that were indexed.
		indexed := make(map[string]struct{}, len(man.Files))
		for _, mf := range man.Files { indexed[mf.Path] = struct{}{} }
		var srcFiles []struct{ RelPath, AbsPath string }
		for _, f := range files {
			if _, ok := indexed[f.RelPath]; ok {
				srcFiles = append(srcFiles, struct{ RelPath, AbsPath string }{RelPath: f.RelPath, AbsPath: f.AbsPath})
			}
		}
		sort.Slice(srcFiles, func(i, j int) bool { return srcFiles[i].RelPath < srcFiles[j].RelPath })

		if err := bundle.WriteChat(*chatFlag, man, srcFiles, syms, g, *chatMaxClasses, *chatMaxChars); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote chat bundle %s (files=%d)\n", *chatFlag, len(man.Files))
		return
	}

	// ======================  FULL MODE  ======================================
	if zipMode {

		// Apply auto-anchor config once.
		index.SetAutoAnchorsConfig(index.AutoAnchorConfig{
			Enabled:        *autoAnchorsFlag,
			MinLines:       *autoAnchorsMin,
			MaxPerFile:     *autoAnchorsMax,
			IncludeImports: *autoAnchorsImports,
			IncludeTests:   *autoAnchorsTests,
			Prefix:         *autoAnchorsPrefix,
		})

		// Build code indices (manifest/symbols/slices/pointers).
		man, syms, slices, pointers := index.BuildArtifacts(srcDir, files, *maxFileLinesFlag, langHints)

		// Enrich manifest from build metadata (Maven/Gradle/Go/Node).
		bi := meta.Detect(srcDir)
		meta.ApplyToManifest(bi, &man)

		// Optional JSON schema validation (stubbed implementation).
		if *validateFlag {
			if err := validate.Manifest(man); err != nil {
				fmt.Fprintln(os.Stderr, "ERROR:", err)
				os.Exit(1)
			}
			if err := validate.Symbols(syms); err != nil {
				fmt.Fprintln(os.Stderr, "ERROR:", err)
				os.Exit(1)
			}
		}

		// Emit only those source files that were indexed (deterministic).
		indexed := make(map[string]struct{}, len(man.Files))
		for _, mf := range man.Files {
			indexed[mf.Path] = struct{}{}
		}
		var srcFiles []struct{ RelPath, AbsPath string }
		if *emitSrcFlag {
			for _, f := range files {
				if _, ok := indexed[f.RelPath]; ok {
					srcFiles = append(srcFiles, struct{ RelPath, AbsPath string }{
						RelPath: f.RelPath, AbsPath: f.AbsPath,
					})
				}
			}
			sort.Slice(srcFiles, func(i, j int) bool { return srcFiles[i].RelPath < srcFiles[j].RelPath })
		}

		// Build lightweight import graph from walked files and pass it to writer.
		gfiles := make([]graph.File, 0, len(files))
		for _, f := range files {
			gfiles = append(gfiles, graph.File{RelPath: f.RelPath, AbsPath: f.AbsPath, Ext: f.Ext})
		}
		g := graph.BuildFrom(gfiles)

		if err := bundle.WriteFull(
			*zipFlag, srcDir, srcFiles, man, syms, slices, pointers, g, *emitSrcFlag,
		); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			os.Exit(1)
		}

		// Persist snapshot so DELTA can compare against this FULL baseline later.
		if *saveSnapOnFull {
			if err := cache.Save(ccDir, curr); err != nil {
				fmt.Fprintln(os.Stderr, "ERROR: saving snapshot:", err)
				os.Exit(1)
			}
		}

		fmt.Printf(
			"Wrote bundle %s (files=%d, symbols=%d, slices=%d, pointers=%d)\n",
			*zipFlag, len(man.Files), len(syms.Symbols), len(slices), len(pointers),
		)
		return
	}

	// Markdown mode is deprecated — instruct to use -zip or -delta.
	if needMarkdown {
		msg := map[string]any{"error": "markdown mode removed; use -zip or -delta"}
		b, _ := json.MarshalIndent(msg, "", "  ")
		fmt.Fprintln(os.Stderr, string(b))
		os.Exit(2)
	}
}
