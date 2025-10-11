// Package main provides the class-collector CLI that scans projects and
// produces FULL, DELTA, or CHAT bundles with deterministic artifacts.
package main

import (
	"bytes"
	"class-collector/internal/bundle"
	"class-collector/internal/cache"
	"class-collector/internal/diff"
	"class-collector/internal/graph"
	"class-collector/internal/index"
	"class-collector/internal/meta"
	"class-collector/internal/validate"
	"class-collector/internal/walkwalk"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type fileRef = struct {
	RelPath string
	AbsPath string
}

type dualFS struct {
	oldRoot string
	newRoot string
}

func (d dualFS) Read(p string, old bool) ([]byte, error) {
	root := d.newRoot
	if old {
		root = d.oldRoot
	}
	full := filepath.Join(root, filepath.FromSlash(p))
	return os.ReadFile(full)
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		logFatal(err)
	}
	opt, langs, err := buildOptions(cfg)
	if err != nil {
		logFatal(err)
	}
	mode, err := selectMode(cfg)
	if err != nil {
		logFatal(err)
	}
	var runErr error
	switch mode {
	case "full":
		runErr = runFull(cfg, opt, langs)
	case "delta":
		runErr = runDelta(cfg, opt)
	case "chat":
		runErr = runChat(cfg, opt)
	default:
		runErr = fmt.Errorf("unknown mode %q", mode)
	}
	if runErr != nil {
		logFatal(runErr)
	}
}

func logFatal(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

// Config holds parsed CLI configuration without side effects. It mirrors the
// existing flags to avoid behavior changes while enabling unit testing.
type Config struct {
	exts           string
	exclude        string
	include        string
	maxBytes       int64
	maxFileBytes   int64
	useGitignore   bool
	followSymlinks bool

	zipOut         string
	deltaOut       string
	chatOut        string
	chatMaxClasses int
	chatMaxChars   int

	diffContext  int
	diffNoPrefix bool

	benchPath string

	tmpDir           string
	resetCache       bool
	storeBlobs       bool
	maxDiffBytes     int
	renameSimilarity bool
	renameSimThresh  int
	renameSimOldRoot string

	emitSrc        bool
	maxFileLines   int
	langHints      string
	validateJSON   bool
	saveSnapOnFull bool

	autoAnchors        bool
	autoAnchorsMin     int
	autoAnchorsMax     int
	autoAnchorsImports bool
	autoAnchorsTests   bool
	autoAnchorsPrefix  string

	srcDir string
}

// parseFlags parses CLI arguments into Config without side effects.
func parseFlags(args []string) (Config, error) {
	var cfg Config
	fs := flag.NewFlagSet("class-collector", flag.ContinueOnError)
	fs.SetOutput(new(bytes.Buffer))

	extsFlag := fs.String("ext",
		".go,.java,.kt,.cs,.ts,.tsx,.js,.json,.yaml,.yml,.xml,.proto,.gradle,.md,.txt,.cpp,.cc,.cxx,.hpp,.hh,.h",
		"comma-separated extensions to include")
	excludeFlag := fs.String("exclude",
		".git,node_modules,dist,build,out,target,.idea,.vscode,.DS_Store",
		"comma-separated dir/file prefixes to exclude")
	includeFlag := fs.String("include", "", "comma-separated substrings to force include (anywhere in path)")
	maxBytesFlag := fs.Int64("max-bytes", 25_000_000, "approximate max total bytes to include in FULL bundle (0 = no limit)")
	maxFileBytesFlag := fs.Int64("max-file-bytes", 2_000_000, "max bytes per file (0 = no limit)")
	useGitignoreFlag := fs.Bool("use-gitignore", true, "honor .gitignore patterns when walking files")
	followSymlinksFlag := fs.Bool("follow-symlinks", false, "follow symlinks during file walk")

	zipFlag := fs.String("zip", "", "path to FULL bundle output (mutually exclusive with -delta/-chat)")
	deltaFlag := fs.String("delta", "", "path to DELTA bundle output (mutually exclusive with -zip/-chat)")
	chatFlag := fs.String("chat", "", "path to CHAT bundle output (mutually exclusive with -zip/-delta)")
	chatMaxClasses := fs.Int("chat-max-classes", 10, "max classes/entities per chat message")
	chatMaxChars := fs.Int("chat-max-chars", 80_000, "max characters per chat message")

	diffContextFlag := fs.Int("diff-context", 4, "lines of context in unified diffs")
	diffNoPrefixFlag := fs.Bool("diff-no-prefix", true, "omit a/ and b/ prefixes in diffs")
	benchFlag := fs.String("bench", "", "path to include as bench.txt in bundles")

	tmpDirFlag := fs.String("tmp-dir", "tmp/.ccache", "base cache directory for snapshots and blobs")
	newFlag := fs.Bool("new", false, "reset cache for this <src_dir> before building")
	storeBlobsFlag := fs.Bool("store-blobs", false, "store source copies as content-addressed blobs for diffs")
	maxDiffBytesFlag := fs.Int("max-diff-bytes", 2_000_000, "max bytes for per-file diffs in DELTA bundles (0 = no limit)")
	renameSimFlag := fs.Bool("rename-similarity", false, "enable similarity-based rename detection in DELTA mode")
	renameSimThreshFlag := fs.Int("rename-sim-thresh", 8, "max Hamming distance for SimHash rename detection")
	renameSimOldRootFlag := fs.String("rename-sim-oldroot", "", "optional root of previous snapshot files for rename similarity")

	emitSrcFlag := fs.Bool("emit-src", false, "include source copies in FULL bundle under src/")
	maxFileLinesFlag := fs.Int("max-file-lines", 500, "max lines per file before slicing; anchors preferred")
	langHintFlag := fs.String("lang", "", "limit symbol extraction to specific languages (comma list)")
	validateFlag := fs.Bool("validate", true, "validate manifest/symbols JSON output")
	saveSnapFlag := fs.Bool("save-snapshot", true, "save snapshot in cache after FULL bundle")

	autoAnchorsFlag := fs.Bool("auto-anchors", true, "generate auto anchors from symbols/imports/tests")
	autoAnchorsMinFlag := fs.Int("auto-anchors-min-lines", 8, "minimum region length for auto anchors")
	autoAnchorsMaxFlag := fs.Int("auto-anchors-max-per-file", 64, "maximum number of auto anchors per file (0 = unlimited)")
	autoAnchorsImportsFlag := fs.Bool("auto-anchors-imports", true, "add IMPORTS anchor when import block exists")
	autoAnchorsTestsFlag := fs.Bool("auto-anchors-tests", true, "add anchors for tests (Go/TS patterns)")
	autoAnchorsPrefixFlag := fs.String("auto-anchors-prefix", "auto:", "prefix for auto anchor names")

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if fs.NArg() < 1 {
		return cfg, fmt.Errorf("missing <src_dir>")
	}

	cfg = Config{
		exts:               *extsFlag,
		exclude:            *excludeFlag,
		include:            *includeFlag,
		maxBytes:           *maxBytesFlag,
		maxFileBytes:       *maxFileBytesFlag,
		useGitignore:       *useGitignoreFlag,
		followSymlinks:     *followSymlinksFlag,
		zipOut:             *zipFlag,
		deltaOut:           *deltaFlag,
		chatOut:            *chatFlag,
		chatMaxClasses:     *chatMaxClasses,
		chatMaxChars:       *chatMaxChars,
		diffContext:        *diffContextFlag,
		diffNoPrefix:       *diffNoPrefixFlag,
		benchPath:          *benchFlag,
		tmpDir:             *tmpDirFlag,
		resetCache:         *newFlag,
		storeBlobs:         *storeBlobsFlag,
		maxDiffBytes:       *maxDiffBytesFlag,
		renameSimilarity:   *renameSimFlag,
		renameSimThresh:    *renameSimThreshFlag,
		renameSimOldRoot:   *renameSimOldRootFlag,
		emitSrc:            *emitSrcFlag,
		maxFileLines:       *maxFileLinesFlag,
		langHints:          *langHintFlag,
		validateJSON:       *validateFlag,
		saveSnapOnFull:     *saveSnapFlag,
		autoAnchors:        *autoAnchorsFlag,
		autoAnchorsMin:     *autoAnchorsMinFlag,
		autoAnchorsMax:     *autoAnchorsMaxFlag,
		autoAnchorsImports: *autoAnchorsImportsFlag,
		autoAnchorsTests:   *autoAnchorsTestsFlag,
		autoAnchorsPrefix:  *autoAnchorsPrefixFlag,
		srcDir:             filepath.Clean(fs.Arg(0)),
	}
	return cfg, nil
}

func buildOptions(cfg Config) (diff.Options, []string, error) {
	opt := diff.Options{
		MaxBytes:       cfg.maxDiffBytes,
		TimeoutSeconds: 5.0,
		Context:        cfg.diffContext,
		NoPrefix:       cfg.diffNoPrefix,
		LineMode:       true,
	}
	langs := []string{"cpp", "cs", "go", "java", "kt", "py", "ts", "tsx"}
	sort.Strings(langs)
	return opt, langs, nil
}

func selectMode(cfg Config) (string, error) {
	zipMode := cfg.zipOut != ""
	deltaMode := cfg.deltaOut != ""
	chatMode := cfg.chatOut != ""
	if (zipMode && deltaMode) || (zipMode && chatMode) || (deltaMode && chatMode) {
		return "", fmt.Errorf("-zip, -delta and -chat are mutually exclusive")
	}
	switch {
	case zipMode:
		return "full", nil
	case deltaMode:
		return "delta", nil
	case chatMode:
		return "chat", nil
	default:
		return "", fmt.Errorf("no mode selected")
	}
}

func runFull(cfg Config, opt diff.Options, _ []string) error {
	files, err := collectFiles(cfg, cfg.maxBytes)
	if err != nil {
		return fmt.Errorf("collect files: %w", err)
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "No files matched filters.")
		return nil
	}

	langHints := toSet(splitCSV(cfg.langHints))
	applyAutoAnchorsConfig(cfg)

	man, syms, slices, pointers := index.BuildArtifacts(cfg.srcDir, files, cfg.maxFileLines, langHints)
	graphFiles := toGraphFiles(files)
	g := graph.BuildFrom(graphFiles)

	meta.ApplyToManifest(meta.Detect(cfg.srcDir), &man)
	if cfg.validateJSON {
		if err := validate.Manifest(man); err != nil {
			return fmt.Errorf("validate manifest: %w", err)
		}
		if err := validate.Symbols(syms); err != nil {
			return fmt.Errorf("validate symbols: %w", err)
		}
	}

	srcFiles := pickIndexedFiles(cfg.emitSrc, files, man)
	if err := bundle.WriteFull(cfg.zipOut, cfg.srcDir, srcFiles, man, syms, slices, pointers, g, cfg.emitSrc, cfg.benchPath, opt.Context, opt.NoPrefix); err != nil {
		return fmt.Errorf("write full bundle: %w", err)
	}
	if err := persistSnapshotOnFull(cfg, man); err != nil {
		return err
	}

	fmt.Printf("Wrote bundle %s (files=%d, symbols=%d, slices=%d, pointers=%d)\n",
		cfg.zipOut, len(man.Files), len(syms.Symbols), len(slices), len(pointers))
	return nil
}

func runDelta(cfg Config, opt diff.Options) error {
	if cfg.maxBytes > 0 {
		fmt.Fprintln(os.Stderr, "Note: ignoring -max-bytes in -delta mode")
	}
	files, err := collectFiles(cfg, 0)
	if err != nil {
		return fmt.Errorf("collect files: %w", err)
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "No files matched filters.")
		return nil
	}

	cacheDir, err := cacheDirFor(cfg)
	if err != nil {
		return err
	}
	if cfg.resetCache {
		if err := cache.Clear(cacheDir); err != nil {
			return fmt.Errorf("clear cache: %w", err)
		}
	}

	curr, err := buildSnapshot(cfg, files)
	if err != nil {
		return err
	}

	prev, err := cache.Load(cacheDir)
	if err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}
	if prev == nil {
		prev = &cache.Snapshot{Module: curr.Module}
	}

	cache.SetRenameSimilarity(cfg.renameSimilarity, cfg.renameSimThresh)
	if cfg.renameSimilarity && cfg.renameSimOldRoot != "" {
		cache.SetContentProvider(dualFS{oldRoot: cfg.renameSimOldRoot, newRoot: cfg.srcDir})
	}

	delta := cache.BuildDelta(prev, curr)
	readOld := func(hash string) ([]byte, error) {
		if len(hash) < 6 {
			return nil, fs.ErrNotExist
		}
		return cache.ReadBlob(cacheDir, hash)
	}
	diffs, err := bundle.MakeDiffs(delta, files, opt, readOld)
	if err != nil {
		return fmt.Errorf("build diffs: %w", err)
	}

	indexPayload := makeDeltaIndex(prev, curr, delta)
	addedFiles := gatherAddedFiles(files, delta.Added)
	if err := bundle.WriteDelta(cfg.deltaOut, indexPayload, diffs, addedFiles, cfg.benchPath, opt.Context, opt.NoPrefix, opt.MaxBytes); err != nil {
		return fmt.Errorf("write delta bundle: %w", err)
	}
	if err := cache.Save(cacheDir, curr); err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}

	fmt.Printf("Wrote delta bundle %s (added=%d, removed=%d, changed=%d, renamed=%d, oversize=%d)\n",
		cfg.deltaOut, len(delta.Added), len(delta.Removed), len(delta.Changed), len(delta.Renamed), countOversize(delta.Changed))
	return nil
}

func runChat(cfg Config, _ diff.Options) error {
	files, err := collectFiles(cfg, cfg.maxBytes)
	if err != nil {
		return fmt.Errorf("collect files: %w", err)
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "No files matched filters.")
		return nil
	}

	langHints := toSet(splitCSV(cfg.langHints))
	applyAutoAnchorsConfig(cfg)

	man, syms, _, _ := index.BuildArtifacts(cfg.srcDir, files, cfg.maxFileLines, langHints)
	graphFiles := toGraphFiles(files)
	g := graph.BuildFrom(graphFiles)

	srcFiles := pickIndexedFiles(true, files, man)
	if err := bundle.WriteChat(cfg.chatOut, man, srcFiles, syms, g, cfg.chatMaxClasses, cfg.chatMaxChars, cfg.benchPath); err != nil {
		return fmt.Errorf("write chat bundle: %w", err)
	}
	fmt.Printf("Wrote chat bundle %s (files=%d)\n", cfg.chatOut, len(man.Files))
	return nil
}

// ------------- helpers -------------

func collectFiles(cfg Config, totalBudget int64) ([]walkwalk.FileInfo, error) {
	exts := toSet(splitCSV(cfg.exts))
	exclude := toSet(splitCSV(cfg.exclude))
	includes := splitCSV(cfg.include)
	files, _, err := walkwalk.CollectFiles(
		cfg.srcDir,
		exts,
		exclude,
		includes,
		totalBudget,
		cfg.maxFileBytes,
		cfg.useGitignore,
		cfg.followSymlinks,
	)
	if err != nil {
		return nil, err
	}
	return files, nil
}

func applyAutoAnchorsConfig(cfg Config) {
	index.SetAutoAnchorsConfig(index.AutoAnchorConfig{
		Enabled:        cfg.autoAnchors,
		MinLines:       cfg.autoAnchorsMin,
		MaxPerFile:     cfg.autoAnchorsMax,
		IncludeImports: cfg.autoAnchorsImports,
		IncludeTests:   cfg.autoAnchorsTests,
		Prefix:         cfg.autoAnchorsPrefix,
	})
}

func toGraphFiles(files []walkwalk.FileInfo) []graph.File {
	out := make([]graph.File, 0, len(files))
	for _, f := range files {
		out = append(out, graph.File{
			RelPath: f.RelPath,
			AbsPath: f.AbsPath,
			Ext:     f.Ext,
		})
	}
	return out
}

func pickIndexedFiles(includeAll bool, files []walkwalk.FileInfo, man index.Manifest) []fileRef {
	if !includeAll {
		return nil
	}
	indexed := make(map[string]struct{}, len(man.Files))
	for _, f := range man.Files {
		indexed[f.Path] = struct{}{}
	}
	out := make([]fileRef, 0, len(indexed))
	for _, f := range files {
		if _, ok := indexed[f.RelPath]; ok {
			out = append(out, fileRef{RelPath: f.RelPath, AbsPath: f.AbsPath})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out
}

func persistSnapshotOnFull(cfg Config, man index.Manifest) error {
	if !cfg.saveSnapOnFull {
		return nil
	}
	cacheDir, err := cacheDirFor(cfg)
	if err != nil {
		return err
	}
	snap := &cache.Snapshot{
		Module:        man.Module,
		Created:       time.Now().UTC().Format(time.RFC3339),
		FormatVersion: "1",
		Files:         make([]cache.SnapFile, 0, len(man.Files)),
	}
	for _, f := range man.Files {
		snap.Files = append(snap.Files, cache.SnapFile{
			Path:  f.Path,
			Hash:  f.Hash,
			Lines: f.Lines,
		})
	}
	if err := cache.Save(cacheDir, snap); err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}
	return nil
}

func cacheDirFor(cfg Config) (string, error) {
	srcAbs, err := filepath.Abs(cfg.srcDir)
	if err != nil {
		return "", fmt.Errorf("abs src dir: %w", err)
	}
	return cache.CacheDir(cfg.tmpDir, srcAbs), nil
}

func buildSnapshot(cfg Config, files []walkwalk.FileInfo) (*cache.Snapshot, error) {
	snap := &cache.Snapshot{
		Module:        filepath.Base(cfg.srcDir),
		Created:       time.Now().UTC().Format(time.RFC3339),
		PrevSrcDir:    "",
		FormatVersion: "1",
		Files:         make([]cache.SnapFile, 0, len(files)),
	}
	cacheDir, err := cacheDirFor(cfg)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		data, err := os.ReadFile(f.AbsPath)
		if err != nil {
			continue
		}
		lines := 1 + bytes.Count(data, []byte("\n"))
		snap.Files = append(snap.Files, cache.SnapFile{
			Path:  f.RelPath,
			Hash:  f.SHA256Hex,
			Lines: lines,
		})
		if cfg.storeBlobs && len(f.SHA256Hex) >= 6 {
			if err := cache.SaveBlob(cacheDir, f.SHA256Hex, bytes.NewReader(data)); err != nil {
				return nil, fmt.Errorf("save blob %s: %w", f.RelPath, err)
			}
		}
	}
	sort.Slice(snap.Files, func(i, j int) bool { return snap.Files[i].Path < snap.Files[j].Path })
	return snap, nil
}

func makeDeltaIndex(prev, curr *cache.Snapshot, delta cache.Delta) any {
	type renamedEntry struct {
		From string `json:"from"`
		To   string `json:"to"`
		Hash string `json:"hash"`
	}
	type changedEntry struct {
		Path       string `json:"path"`
		HashBefore string `json:"hashBefore"`
		HashAfter  string `json:"hashAfter"`
		Diff       string `json:"diff"`
		Oversize   bool   `json:"oversize"`
	}
	renamed := make([]renamedEntry, 0, len(delta.Renamed))
	for _, r := range delta.Renamed {
		renamed = append(renamed, renamedEntry{From: r.From, To: r.To, Hash: r.Hash})
	}
	changed := make([]changedEntry, 0, len(delta.Changed))
	for _, c := range delta.Changed {
		changed = append(changed, changedEntry{
			Path:       c.Path,
			HashBefore: c.HashBefore,
			HashAfter:  c.HashAfter,
			Diff:       c.DiffPath,
			Oversize:   c.Oversize,
		})
	}
	return struct {
		BaseModule   string           `json:"baseModule"`
		BaseSnapshot string           `json:"baseSnapshot"`
		HeadSnapshot string           `json:"headSnapshot"`
		Added        []cache.SnapFile `json:"added"`
		Removed      []cache.SnapFile `json:"removed"`
		Renamed      []renamedEntry   `json:"renamed"`
		Changed      []changedEntry   `json:"changed"`
	}{
		BaseModule:   curr.Module,
		BaseSnapshot: prev.Created,
		HeadSnapshot: curr.Created,
		Added:        append([]cache.SnapFile{}, delta.Added...),
		Removed:      append([]cache.SnapFile{}, delta.Removed...),
		Renamed:      renamed,
		Changed:      changed,
	}
}

func gatherAddedFiles(files []walkwalk.FileInfo, added []cache.SnapFile) []fileRef {
	if len(added) == 0 {
		return nil
	}
	byRel := make(map[string]string, len(files))
	for _, f := range files {
		byRel[f.RelPath] = f.AbsPath
	}
	out := make([]fileRef, 0, len(added))
	for _, a := range added {
		if abs, ok := byRel[a.Path]; ok {
			out = append(out, fileRef{RelPath: a.Path, AbsPath: abs})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out
}

func countOversize(changed []struct {
	Path       string `json:"path"`
	HashBefore string `json:"hashBefore"`
	HashAfter  string `json:"hashAfter"`
	DiffPath   string `json:"diff"`
	Oversize   bool   `json:"oversize"`
}) int {
	n := 0
	for _, c := range changed {
		if c.Oversize {
			n++
		}
	}
	return n
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func toSet(list []string) map[string]struct{} {
	if len(list) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(list))
	for _, v := range list {
		if v != "" {
			m[v] = struct{}{}
		}
	}
	return m
}
