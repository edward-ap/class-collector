// Package meta detects build/runtime metadata of a project (Maven/Gradle/Go/Node)
// and applies the results to the bundle manifest.
//
// Goals:
//   - Zero external dependencies (stdlib only)
//   - Best-effort parsing: tolerate partial/absent files
//   - Deterministic defaults for SourceGlobs/Entrypoints per build type
package meta

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"class-collector/internal/index"
)

// Info contains a minimal, tool-friendly summary of build metadata.
type Info struct {
	Build       string   // "maven"|"gradle"|"go"|"node"|"" (unknown)
	JDK         string   // e.g., "21", "17"
	Module      string   // artifact/module/package name (best-effort)
	Entrypoints []string // e.g., ["org.acme.Main"], ["dist/index.js"]
	SourceGlobs []string // e.g., ["src/main/java/**/*.java", "src/test/java/**/*.java"]
}

// Detect collects build metadata by probing common files in the project root:
//
// Priority (first match wins for Build): Maven > Gradle > Go > Node
func Detect(root string) Info {
	absRoot, _ := filepath.Abs(root)

	// 1) Maven (pom.xml)
	if p := firstExisting(absRoot, "pom.xml"); p != "" {
		if inf, ok := detectMaven(absRoot, p); ok {
			return inf
		}
	}

	// 2) Gradle (build.gradle, build.gradle.kts, settings.gradle(.kts))
	if p := firstExisting(absRoot, "build.gradle", "build.gradle.kts"); p != "" {
		if inf, ok := detectGradle(absRoot, p); ok {
			return inf
		}
	}

	// 3) Go (go.mod)
	if p := firstExisting(absRoot, "go.mod"); p != "" {
		if inf, ok := detectGo(absRoot, p); ok {
			return inf
		}
	}

	// 4) Node (package.json)
	if p := firstExisting(absRoot, "package.json"); p != "" {
		if inf, ok := detectNode(absRoot, p); ok {
			return inf
		}
	}

	return Info{} // unknown
}

// ApplyToManifest merges detected Info into the manifest without overriding
// non-empty fields already set upstream (best-effort enrichment).
func ApplyToManifest(inf Info, m *index.Manifest) {
	if m == nil {
		return
	}
	if m.Build == "" && inf.Build != "" {
		m.Build = inf.Build
	}
	if m.JDK == "" && inf.JDK != "" {
		m.JDK = inf.JDK
	}
	if m.Module == "" && inf.Module != "" {
		m.Module = inf.Module
	}
	if len(m.Entrypoints) == 0 && len(inf.Entrypoints) > 0 {
		m.Entrypoints = append([]string(nil), inf.Entrypoints...)
	}
	if len(m.SourceGlobs) == 0 && len(inf.SourceGlobs) > 0 {
		m.SourceGlobs = append([]string(nil), inf.SourceGlobs...)
	}
}

// ------------------------------ Maven ----------------------------------------

type pomXML struct {
	XMLName    xml.Name  `xml:"project"`
	GroupID    string    `xml:"groupId"`
	ArtifactID string    `xml:"artifactId"`
	Version    string    `xml:"version"`
	Parent     pomParent `xml:"parent"`
	Props      pomProps  `xml:"properties"`
}

type pomParent struct {
	GroupID string `xml:"groupId"`
	Version string `xml:"version"`
}

type pomProps struct {
	Source  string `xml:"maven.compiler.source"`
	Target  string `xml:"maven.compiler.target"`
	Release string `xml:"maven.compiler.release"`
	JavaVer string `xml:"java.version"`
}

func detectMaven(root, pomPath string) (Info, bool) {
	b, err := os.ReadFile(pomPath)
	if err != nil {
		return Info{}, false
	}
	var p pomXML
	if err := xml.Unmarshal(b, &p); err != nil {
		return Info{}, false
	}

	group := firstNonEmpty(p.GroupID, p.Parent.GroupID)
	artifact := p.ArtifactID
	version := firstNonEmpty(p.Version, p.Parent.Version)

	jdk := firstNonEmpty(p.Props.Release, p.Props.Target, p.Props.Source, p.Props.JavaVer)
	jdk = normalizeJDK(jdk)

	mod := artifact
	if mod == "" {
		// fall back to directory name
		mod = filepath.Base(root)
	}

	// Maven defaults for source layout
	globs := []string{"src/main/java/**/*.java", "src/test/java/**/*.java"}

	// Entrypoints are not explicitly declared in Maven POM. Leave empty.
	_ = version
	_ = group

	return Info{
		Build:       "maven",
		JDK:         jdk,
		Module:      mod,
		Entrypoints: nil,
		SourceGlobs: globs,
	}, true
}

// ------------------------------ Gradle ---------------------------------------

func detectGradle(root, buildPath string) (Info, bool) {
	b, err := os.ReadFile(buildPath)
	if err != nil {
		return Info{}, false
	}
	text := string(b)

	// Heuristics: look for sourceCompatibility/targetCompatibility = "21"/JavaVersion.VERSION_21 etc.
	jdk := ""
	if m := reGradleCompatQuoted.FindStringSubmatch(text); m != nil {
		jdk = normalizeJDK(m[1])
	} else if m := reGradleCompatEnum.FindStringSubmatch(text); m != nil {
		jdk = normalizeJDK(m[1])
	}

	// Try gradle.properties for org.gradle.java.home or java version hints
	if p := firstExisting(root, "gradle.properties"); p != "" && jdk == "" {
		if v := scanGradlePropertiesForJavaVersion(p); v != "" {
			jdk = normalizeJDK(v)
		}
	}

	// Module name: settings.gradle(.kts) → rootProject.name = 'foo'
	mod := ""
	if p := firstExisting(root, "settings.gradle", "settings.gradle.kts"); p != "" {
		if v := scanSettingsGradleForRootName(p); v != "" {
			mod = v
		}
	}
	if mod == "" {
		mod = filepath.Base(root)
	}

	globs := []string{
		"src/main/java/**/*.java",
		"src/test/java/**/*.java",
		"src/main/kotlin/**/*.kt",
		"src/test/kotlin/**/*.kt",
	}

	return Info{
		Build:       "gradle",
		JDK:         jdk,
		Module:      mod,
		Entrypoints: nil,
		SourceGlobs: globs,
	}, true
}

var (
	reGradleCompatQuoted = regexp.MustCompile(`(?m)^\s*(?:sourceCompatibility|targetCompatibility)\s*=\s*["']?(\d{1,2})["']?`)
	reGradleCompatEnum   = regexp.MustCompile(`(?m)^\s*(?:sourceCompatibility|targetCompatibility)\s*=\s*JavaVersion\.VERSION_(\d{1,2})`)
	reGradleRootName     = regexp.MustCompile(`(?m)^\s*rootProject\.name\s*=\s*["']([^"']+)["']`)
)

func scanSettingsGradleForRootName(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if m := reGradleRootName.FindStringSubmatch(string(b)); m != nil {
		return m[1]
	}
	return ""
}

func scanGradlePropertiesForJavaVersion(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// very light scan for commonly used properties
	lines := strings.Split(string(b), "\n")
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		kv := strings.SplitN(ln, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		switch key {
		case "org.gradle.java.home", "java.version", "jdk":
			if v := normalizeJDK(val); v != "" {
				return v
			}
		}
	}
	return ""
}

// ------------------------------ Go ------------------------------------------

func detectGo(root, modPath string) (Info, bool) {
	b, err := os.ReadFile(modPath)
	if err != nil {
		return Info{}, false
	}
	mod, _ := parseGoMod(string(b))

	module := mod
	if module == "" {
		module = filepath.Base(root)
	}
	// There's no JDK in Go projects; keep empty.
	return Info{
		Build:       "go",
		JDK:         "",
		Module:      module,
		Entrypoints: nil, // discovering "main" packages would require scanning; skip
		SourceGlobs: []string{"**/*.go"},
	}, true
}

func parseGoMod(text string) (module, goVer string) {
	lines := strings.Split(text, "\n")
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "module ") {
			module = strings.TrimSpace(strings.TrimPrefix(ln, "module "))
		} else if strings.HasPrefix(ln, "go ") {
			goVer = strings.TrimSpace(strings.TrimPrefix(ln, "go "))
		}
	}
	module = strings.TrimSpace(module)
	goVer = strings.TrimSpace(goVer)
	return
}

// ------------------------------ Node ----------------------------------------

func detectNode(root, pkgPath string) (Info, bool) {
	b, err := os.ReadFile(pkgPath)
	if err != nil {
		return Info{}, false
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return Info{}, false
	}

	name := strField(obj, "name")
	main := strField(obj, "main")
	module := strField(obj, "module") // ESM entry
	// prefer "module" (ESM), then "main" (CJS)
	entry := firstNonEmpty(module, main)

	entries := []string(nil)
	if entry != "" {
		entries = []string{entry}
	}

	return Info{
		Build:       "node",
		JDK:         "", // not applicable
		Module:      firstNonEmpty(name, filepath.Base(root)),
		Entrypoints: entries,
		SourceGlobs: []string{"src/**/*.{ts,tsx,js,jsx}", "lib/**/*.{ts,tsx,js,jsx}"},
	}, true
}

// ---------------------------- helpers ---------------------------------------

func firstExisting(root string, names ...string) string {
	for _, n := range names {
		p := filepath.Join(root, n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s != "" {
			return s
		}
	}
	return ""
}

// normalizeJDK tries to coerce input like "21", "1.8", "17.0.1" into "21"|"17"|"8".
func normalizeJDK(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// "1.8" -> "8"; "17.0.1" -> "17"
	if strings.HasPrefix(s, "1.") && len(s) >= 3 {
		return strings.TrimPrefix(s, "1.")
	}
	// keep leading digits until first non-digit
	out := strings.Builder{}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func strField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		switch t := v.(type) {
		case string:
			return strings.TrimSpace(t)
		default:
			// tolerate numbers/bools by stringifying
			return strings.TrimSpace(toString(v))
		}
	}
	return ""
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

// Optional helper: returns true if the file exists and is not a directory.
func existsFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
