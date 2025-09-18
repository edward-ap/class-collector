# class-collector — reproducible code bundles for LLMs

> **TL;DR**: Large Language Models struggle with big repositories because chats and APIs have hard limits — either on uploaded files, context length, or session tokens. Naïvely re-sending whole files wastes capacity and money.
>
> class-collector builds deterministic FULL bundles once, then ships compact DELTA bundles with only the relevant diffs. This keeps sessions lean, reproducible, and cost-efficient. 

---

## Why not just upload files?

Most LLMs can’t handle large codebases directly:

* Upload caps (file size / number of files) in many chat UIs.
* Context limits in APIs: tens of thousands of tokens max, easily exhausted by a few big files.
* Inefficiency: Re-uploading the whole tree for each change burns tokens and time.
* Instead, the right approach is:
* Ship a FULL snapshot once for structure.
* On every edit, provide only the DELTA (diffs, added/removed files).
* Use pointers and slices so the model sees just the relevant region, not entire megabyte-long files.

---

## Why not just concatenate classes?

Plain concatenation doesn’t help an LLM understand **structure**. Large files become un-navigable, symbol boundaries get lost, and every iteration re-sends megabytes of nearly-unchanged text. Without diffs, subsequent updates **burn context and tokens** — especially if you pay per token via API. Structured, reproducible bundles fix that.

---

## How to prepare code for an LLM (quick theory)

1. **Index structure, not just text.**  
   Record per-file metadata (package/type), exported symbols, explicit/virtual anchors.

2. **Split long files.**  
   If there are no manual `region` / `#region` markers, synthesize **auto‑anchors** (imports, tests, etc.) and cut the file into **slices** (chunked regions) at a sensible line limit.

3. **Give the model jump points.**  
   Build **pointers** — stable jump IDs for anchors and symbols. Then you can send only the relevant slice instead of the entire file.

4. **After the first FULL, send only DELTAs.**  
   Ship `delta.zip` containing a structured change index + unified patches + newly-added files. The model starts from the previous context and moves faster.

5. **Minimize noise.**  
   Exclude build artifacts (`node_modules`, `dist`, `build`, etc.), keep outputs deterministic (stable ordering, fixed ZIP timestamps).

---

## What `class-collector` does


- **Deterministic walk** of the repo (filters, symlink policy, .gitignore support, size guardrails).
- Builds **`manifest.json`** with file metadata (package, type, exports, anchors, hash, line count).
- Extracts **symbols** (Java, Go, TS/JS, Kotlin, C#, Python) and generates stable pointers.
- Synthesizes **auto-anchors** (imports, tests, consts/types/funcs, fields/ctors/methods) for coarse navigation.
- Constructs an **`import graph`** (Java, Go, TS/JS with tsconfig paths, CJS require).
- Produces **`slices.jsonl`** — line-delimited slices (anchors or chunked regions) for long files.
- Writes a **reproducible ZIP** (fixed timestamps, sorted entries, sanitized paths).
- Maintains a **snapshot** under `tmp/.ccache` and emits **DELTA archives** with:
    - `delta.index.json` (added/removed/renamed/changed),
    - `diffs/*.patch` (unified patches),
    - `added/<path>` (bodies of new files).
- Supports similarity-based rename detection (SimHash).
- Optionally includes a `src/` tree inside FULL bundles (`-emit-src`).

---

## Time, context, and cost savings

- **One-time FULL** gives the model a structured overview of the codebase.  
- **Subsequent DELTAs** ship only changes — often **hundreds of kilobytes** instead of tens of megabytes.  
- **Fewer tokens → lower bills**, particularly with API-driven reviews and iterative refactors.  
- **Lower cognitive load**: pointers, anchors, and the import graph give concrete “entry points.”

---

## Installation

Requires Go 1.21+.

```bash
# from the repo root:
go build -o class-collector ./cmd/class-collector

# (optional)
go install ./cmd/class-collector
```

---

## Quick start

### 1) Build the initial FULL bundle

```bash
class-collector -zip out/full.zip -store-blobs -emit-src ./path/to/project
```

This produces `out/full.zip` with a stable structure:
```
manifest.json
symbols.json
slices.jsonl
pointers.jsonl
graph.json
README.md
TOC.md
src/...          # if -emit-src was provided
```
A snapshot is stored under `tmp/.ccache/<key>/`. With `-store-blobs`, source blobs are kept for high-fidelity diffs.

### 2) Generate a DELTA after you change code

```bash
class-collector -delta out/delta.2025-09-16.zip ./path/to/project
```

You’ll get:
```
delta.index.json    # change metadata
diffs/*.patch       # unified diffs for Changed (size capped by -max-diff-bytes)
added/<path>        # full content of newly added files
```

> Tip: In **DELTA mode**, the tool **ignores `-max-bytes`** to inspect all candidates for accurate change detection.

---

## CLI — flags and modes

### Modes
- `-zip <file>` — build a **FULL** bundle (mutually exclusive with `-delta`).  
- `-delta <file>` — build a **DELTA** bundle (mutually exclusive with `-zip`).
- `-chat <file>` — Chat packetizer bundle.

Positional arg: `<src_dir>` — project root to scan.

### Flags (from the current CLI)
| Flag | Type | Default | Description |
|---|---|---|---|
| `-include` | string | `""` | comma-separated substrings to force-include (in path) |
| `-max-bytes` | int64 | `25_000_000` | approx max total bytes to include in FULL mode (0 = no limit) |
| `-follow-symlinks` | bool | `false` | follow symlinks during walk |
| `-zip` | string | `""` | path to output FULL zip bundle (mutually exclusive with -delta) |
| `-delta` | string | `""` | path to output DELTA zip bundle (mutually exclusive with -zip) |
| `-tmp-dir` | string | `"tmp/.ccache"` | base cache directory for snapshots and blobs |
| `-new` | bool | `false` | reset cache for this <src_dir> before building |
| `-store-blobs` | bool | `false` | store source copies as content-addressed blobs for diffs |
| `-max-diff-bytes` | int | `2_000_000` | max bytes for diffs in -delta (0 = no limit) |
| `-emit-src` | bool | `false` | include source copies in the FULL zip under src/ |
| `-max-file-lines` | int | `500` | max lines per file before slicing; anchors preferred |
| `-lang` | string | `""` | limit symbol extraction to languages (comma list: java,go,ts,tsx,js) |
| `-validate` | bool | `true` | validate manifest/symbols JSON against schemas (if available) |
| `-save-snapshot` | bool | `true` | save snapshot in tmp after FULL (-zip) |
| `-auto-anchors` | bool | `true` | synthesize virtual anchors from symbols/imports/tests |
| `-auto-anchors-min-lines` | int | `8` | minimum region length for auto anchors |
| `-auto-anchors-max-per-file` | int | `64` | maximum number of auto anchors per file (0 = unlimited) |
| `-auto-anchors-imports` | bool | `true` | add IMPORTS anchor if an import block exists |
| `-auto-anchors-tests` | bool | `true` | add test anchors (Go: Test*/Benchmark*/Example*, TS: describe/it/test) |
| `-auto-anchors-prefix` | string | `"auto:"` | prefix for auto anchor names |

---

## Examples

### Only Java + TS with strict excludes

```bash
class-collector   -zip out/java-ts.full.zip   -ext ".java,.ts,.tsx"   -exclude ".git,node_modules,dist,build"   -lang "java,ts,tsx"   ./my-project
```

### FULL with source copies and blobs for future diffs

```bash
class-collector -zip out/full.zip -emit-src -store-blobs ./repo
```

### DELTA without a per-patch size cap (use cautiously)

```bash
class-collector -delta out/delta.zip -max-diff-bytes 0 ./repo
```

### Reset snapshot and rebuild from scratch

```bash
class-collector -new -zip out/full.zip ./repo
```

---

## Bundle layout

### FULL ZIP
- **`manifest.json`** — indexed files with: `path`, `package`, `class`, `kind`, `exports[]`, `hash`, `lines`, `anchors[]`  
- **`symbols.json`** — symbol list (Java/Go/TS/JS) with 1‑based line ranges  
- **`slices.jsonl`** — one JSON object per slice (anchor-based or chunked)  
- **`pointers.jsonl`** — stable jump pointers (anchors and symbols)  
- **`graph.json`** — import graph (deterministic nodes/edges)  
- **`README.md`** and **`TOC.md`** — stable overview artifacts  
- **`src/`** — optional, sources included in a fixed order

### DELTA ZIP
- **`delta.index.json`** — change summary, e.g.:
```json
{
  "baseModule": "my-app",
  "baseSnapshot": "2025-09-15T21:49:12Z",
  "headSnapshot": "2025-09-16T10:03:00Z",
  "added":   [{ "path": "pkg/X.java", "hash": "...", "lines": 123 }],
  "removed": [{ "path": "pkg/Y.java", "hash": "...", "lines": 77 }],
  "renamed": [{ "from": "old/A.go", "to": "new/A.go", "hash": "..." }],
  "changed": [{ 
    "path": "core/service.ts",
    "hashBefore": "...",
    "hashAfter":  "...",
    "diff": "diffs/core_service_ts_<hash>.patch",
    "oversize": false
  }]
}
```
- **`diffs/*.patch`** — unified patches (when the previous blob is available)  
- **`added/<path>`** — full content of newly added files

---

## Using bundles with Chat/API

1. First, upload the **FULL ZIP** and feed the model **`README.md` + `TOC.md`** for overview, pulling **specific slices** from `slices.jsonl` / `pointers.jsonl` on demand.  
2. On each iteration, send a **DELTA ZIP** — the model sees *what changed* via `delta.index.json` and the patches.  
3. Ask the model for **specific regions by pointer ID** rather than entire files to stay under message limits (e.g., “≤ 10 classes per message”) and reduce tokens.

---

## Limitations / Notes

- The TS/JS extractor targets `export class/interface`, `export function`, and common arrow exports (`export const Name = (...) =>`).  
  **Default function exports**, **re‑exports** (`export { Foo } from '...'`), and some `var/let` exports aren’t recognized as symbols in this version.
- Kotlin/C# files are included in the manifest/slices but **don’t** emit symbols yet.  
- Rename detection relies on **identical hashes**; moved‑and‑modified files appear as remove+add+patch.  
- `.gitignore` is **not** evaluated; use `-exclude`/`-include` filters.  
- Validation is lightweight and deterministic (not a full JSON‑Schema validator).  
- The legacy “Markdown mode” is removed — use `-zip` or `-delta`.

---

## Contributing

- Zero extra deps (except `github.com/pmezard/go-difflib/difflib` for patches).  
- Deterministic everywhere: sorting, fixed timestamps, sanitized ZIP paths.  
- PRs welcome: TS symbols, Kotlin/C#, `.gitignore`, rename heuristics, chat‑packetizer, etc.

---

> *If you interact with an LLM via API, switching from “re-upload everything” to “one FULL + subsequent DELTAs” usually pays off on day one: fewer tokens, faster iterations, higher signal‑to‑noise.*
