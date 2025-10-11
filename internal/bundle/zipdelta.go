// Package bundle contains writers for full and delta bundles.
// This file implements the DELTA bundle writer with deterministic ordering.
package bundle

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"class-collector/internal/diff"
	"class-collector/internal/sortutil"
	"class-collector/internal/textutil"
	"class-collector/internal/ziputil"
)

type zipPatch struct {
	name string
	body []byte
}

type deltaView struct {
	BaseModule string
	Added      []string
	Removed    []string
	Renamed    []struct {
		From string
		To   string
	}
	Changed []struct {
		Path     string
		DiffPath string
		Oversize bool
	}
}

// prepareDeltaView converts an arbitrary JSON-serialisable delta index into a
// normalized view used for README/SUMMARY generation.
func prepareDeltaView(deltaIndex any) deltaView {
	if deltaIndex == nil {
		return deltaView{}
	}
	var raw struct {
		BaseModule string `json:"baseModule"`
		Added      []struct {
			Path string `json:"path"`
		} `json:"added"`
		Removed []struct {
			Path string `json:"path"`
		} `json:"removed"`
		Renamed []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"renamed"`
		Changed []struct {
			Path     string `json:"path"`
			DiffPath string `json:"diff"`
			Oversize bool   `json:"oversize"`
		} `json:"changed"`
	}
	view := deltaView{}
	if b, err := json.Marshal(deltaIndex); err == nil {
		_ = json.Unmarshal(b, &raw)
	}
	view.BaseModule = raw.BaseModule
	for _, a := range raw.Added {
		view.Added = append(view.Added, a.Path)
	}
	for _, r := range raw.Removed {
		view.Removed = append(view.Removed, r.Path)
	}
	for _, rn := range raw.Renamed {
		view.Renamed = append(view.Renamed, struct {
			From string
			To   string
		}{From: rn.From, To: rn.To})
	}
	for _, ch := range raw.Changed {
		view.Changed = append(view.Changed, struct {
			Path     string
			DiffPath string
			Oversize bool
		}{Path: ch.Path, DiffPath: ch.DiffPath, Oversize: ch.Oversize})
	}
	view.Added = sortutil.StablePathSort(view.Added)
	view.Removed = sortutil.StablePathSort(view.Removed)
	sort.Slice(view.Renamed, func(i, j int) bool {
		if view.Renamed[i].From == view.Renamed[j].From {
			return view.Renamed[i].To < view.Renamed[j].To
		}
		return view.Renamed[i].From < view.Renamed[j].From
	})
	sort.Slice(view.Changed, func(i, j int) bool {
		return view.Changed[i].Path < view.Changed[j].Path
	})
	return view
}

func writePerFileDiffs(zw *zip.Writer, diffs map[string]string) ([]zipPatch, error) {
	if len(diffs) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(diffs))
	for name := range diffs {
		names = append(names, name)
	}
	names = sortutil.StablePathSort(names)

	used := make(map[string]struct{}, len(names))
	out := make([]zipPatch, 0, len(names))
	for _, name := range names {
		raw := filepath.ToSlash(filepath.Join("diffs", name))
		zname := ziputil.EnsureUniqueName(ziputil.SanitizePath(raw), used)
		body := []byte(diffs[name])
		norm := textutil.EnsureTrailingLF(textutil.NormalizeUTF8LF(body))
		if err := ziputil.WriteText(zw, zname, norm); err != nil {
			return nil, fmt.Errorf("write %s: %w", zname, err)
		}
		out = append(out, zipPatch{name: zname, body: norm})
	}
	return out, nil
}

func synthesizeAddedPatches(files []struct{ RelPath, AbsPath string }, maxBytes, diffContext int, diffNoPrefix bool) ([]zipPatch, error) {
	if len(files) == 0 {
		return nil, nil
	}
	opt := diff.Options{
		MaxBytes: maxBytes,
		Context:  diffContext,
		NoPrefix: diffNoPrefix,
		LineMode: true,
	}
	out := make([]zipPatch, 0, len(files))
	for _, f := range files {
		data, err := os.ReadFile(f.AbsPath)
		if err != nil {
			continue
		}
		bName := filepath.ToSlash(f.RelPath)
		if !diffNoPrefix {
			bName = "b/" + bName
		}
		body, _ := diff.Added(bName, data, opt)
		norm := textutil.EnsureTrailingLF(textutil.NormalizeUTF8LF([]byte(body)))
		out = append(out, zipPatch{
			name: filepath.ToSlash(filepath.Join("added", f.RelPath)),
			body: norm,
		})
	}
	return out, nil
}

func buildDeltaPatch(perFile, added []zipPatch) []byte {
	if len(perFile) == 0 && len(added) == 0 {
		return nil
	}
	all := make([]zipPatch, 0, len(perFile)+len(added))
	all = append(all, perFile...)
	all = append(all, added...)
	sort.Slice(all, func(i, j int) bool {
		return all[i].name < all[j].name
	})
	chunks := make([][]byte, 0, len(all))
	for _, p := range all {
		chunks = append(chunks, p.body)
	}
	joined := textutil.JoinWithSingleNL(chunks...)
	return textutil.EnsureTrailingLF(textutil.NormalizeUTF8LF(joined))
}

func writeSummary(zw *zip.Writer, view deltaView) error {
	var b strings.Builder
	b.WriteString("# SUMMARY\n\n")
	fmt.Fprintf(&b, "Changed (%d):\n", len(view.Changed))
	for _, c := range view.Changed {
		target := c.DiffPath
		if target == "" {
			target = "diffs/"
		}
		fmt.Fprintf(&b, "- %s -> %s\n", c.Path, target)
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "Added (%d):\n", len(view.Added))
	for _, path := range view.Added {
		fmt.Fprintf(&b, "- %s -> added/%s\n", path, path)
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "Removed (%d):\n", len(view.Removed))
	for _, path := range view.Removed {
		fmt.Fprintf(&b, "- %s\n", path)
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "Renamed (%d):\n", len(view.Renamed))
	for _, rn := range view.Renamed {
		fmt.Fprintf(&b, "- %s -> %s\n", rn.From, rn.To)
	}
	b.WriteString("\n")

	oversize := 0
	for _, c := range view.Changed {
		if c.Oversize {
			oversize++
		}
	}
	fmt.Fprintf(&b, "Oversize diffs (%d)\n", oversize)

	text := textutil.EnsureTrailingLF(textutil.NormalizeUTF8LF([]byte(b.String())))
	if err := ziputil.WriteText(zw, "SUMMARY.md", text); err != nil {
		return fmt.Errorf("write SUMMARY.md: %w", err)
	}
	return nil
}

func writeReadme(zw *zip.Writer, view deltaView, benchPath string, diffContext int, diffNoPrefix bool, present []string) error {
	readme := GenerateDeltaReadme(ReadmeOptions{
		ModuleName:        view.BaseModule,
		SupportedLangs:    supportedLangs(),
		PresentLangs:      present,
		DiffNoPrefix:      diffNoPrefix,
		ContextLines:      diffContext,
		IncludeBenchNote:  strings.TrimSpace(benchPath) != "",
		IncludeDeltaNotes: true,
	})
	readme = textutil.EnsureTrailingLF(textutil.NormalizeUTF8LF(readme))
	if err := ziputil.WriteText(zw, "README.md", readme); err != nil {
		return fmt.Errorf("write README.md: %w", err)
	}
	return nil
}

func maybeWriteBench(zw *zip.Writer, benchPath string) error {
	if strings.TrimSpace(benchPath) == "" {
		return nil
	}
	data, err := os.ReadFile(benchPath)
	if err != nil {
		return fmt.Errorf("read bench.txt: %w", err)
	}
	if err := ziputil.WriteFile(zw, "bench.txt", data); err != nil {
		return fmt.Errorf("write bench.txt: %w", err)
	}
	return nil
}

// WriteDelta writes a delta ZIP archive with deterministic layout.
func WriteDelta(
	zipPath string,
	deltaIndex any,
	diffs map[string]string,
	addedFiles []struct{ RelPath, AbsPath string },
	benchPath string,
	diffContext int,
	diffNoPrefix bool,
	maxDiffBytes int,
) error {
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return fmt.Errorf("mkdir output: %w", err)
	}
	f, err := os.Create(zipPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	if err := ziputil.WriteJSON(zw, "delta.index.json", deltaIndex); err != nil {
		return fmt.Errorf("write delta.index.json: %w", err)
	}

	perFile, err := writePerFileDiffs(zw, diffs)
	if err != nil {
		return err
	}
	addedPatches, err := synthesizeAddedPatches(addedFiles, maxDiffBytes, diffContext, diffNoPrefix)
	if err != nil {
		return err
	}
	if patch := buildDeltaPatch(perFile, addedPatches); len(patch) > 0 {
		if err := ziputil.WriteText(zw, "delta.patch", patch); err != nil {
			return fmt.Errorf("write delta.patch: %w", err)
		}
	}

	if len(addedFiles) > 0 {
		sorted := make([]struct{ RelPath, AbsPath string }, len(addedFiles))
		copy(sorted, addedFiles)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].RelPath < sorted[j].RelPath
		})
		used := make(map[string]struct{}, len(sorted))
		for _, f := range sorted {
			raw := filepath.ToSlash(filepath.Join("added", f.RelPath))
			zname := ziputil.EnsureUniqueName(ziputil.SanitizePath(raw), used)
			data, err := os.ReadFile(f.AbsPath)
			if err != nil {
				return fmt.Errorf("read added file %s: %w", f.AbsPath, err)
			}
			if err := ziputil.WriteFile(zw, zname, data); err != nil {
				return fmt.Errorf("write %s: %w", zname, err)
			}
		}
	}

	view := prepareDeltaView(deltaIndex)
	if err := writeSummary(zw, view); err != nil {
		return err
	}

	// present — сначала из view, а если пусто, добираем из added+diffs
	present := presentLangsFromDelta(view)
	if len(present) == 0 {
		present = presentLangsFromAddedAndDiffs(addedFiles, perFile)
	}

	if err := writeReadme(zw, view, benchPath, diffContext, diffNoPrefix, present); err != nil {
		return err
	}
	if err := maybeWriteBench(zw, benchPath); err != nil {
		return err
	}
	return nil
}

func presentLangsFromDelta(view deltaView) []string {
	m := map[string]struct{}{}

	add := func(p string) {
		ext := strings.ToLower(filepath.Ext(p))
		switch ext {
		case ".go":
			m["go"] = struct{}{}
		case ".java":
			m["java"] = struct{}{}
		case ".kt":
			m["kt"] = struct{}{}
		case ".cs":
			m["cs"] = struct{}{}
		case ".ts":
			m["ts"] = struct{}{}
		case ".tsx":
			m["tsx"] = struct{}{}
		case ".py":
			m["py"] = struct{}{}
		case ".cpp", ".cc", ".cxx", ".hpp", ".hh", ".h":
			m["cpp"] = struct{}{}
		}
	}

	for _, a := range view.Added {
		add(a)
	}
	for _, r := range view.Removed {
		add(r)
	}
	for _, c := range view.Changed {
		add(c.Path)
	}

	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// presentLangsFromAddedAndDiffs извлекает языки из added-файлов и из заголовков пофайловых патчей.
func presentLangsFromAddedAndDiffs(added []struct{ RelPath, AbsPath string }, perFile []zipPatch) []string {
	m := map[string]struct{}{}

	addPath := func(p string) {
		ext := strings.ToLower(filepath.Ext(p))
		switch ext {
		case ".go":
			m["go"] = struct{}{}
		case ".java":
			m["java"] = struct{}{}
		case ".kt":
			m["kt"] = struct{}{}
		case ".cs":
			m["cs"] = struct{}{}
		case ".ts":
			m["ts"] = struct{}{}
		case ".tsx":
			m["tsx"] = struct{}{}
		case ".py":
			m["py"] = struct{}{}
		case ".cpp", ".cc", ".cxx", ".hpp", ".hh", ".h":
			m["cpp"] = struct{}{}
		}
	}

	// 1) added/*
	for _, f := range added {
		addPath(f.RelPath)
	}

	// 2) diffs/* — парсим заголовки '+++ <path>'
	for _, p := range perFile {
		// ищем первую строку, начинающуюся с '+++ '
		body := string(p.body)
		if i := strings.Index(body, "+++ "); i >= 0 {
			// взять строку до перевода строки
			line := body[i:]
			if j := strings.IndexByte(line, '\n'); j >= 0 {
				line = line[:j]
			}
			// срез после '+++ '
			path := strings.TrimSpace(line[4:])
			// убрать возможный 'b/' префикс
			if strings.HasPrefix(path, "b/") {
				path = path[2:]
			}
			// иногда встречаются служебные '<old>/<new>' — фильтруем только нормальные пути
			if !strings.HasPrefix(path, "<") && path != "/dev/null" {
				addPath(path)
			}
		}
	}

	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
