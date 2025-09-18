// Package cache provides snapshot and on-disk cache utilities used by the
// incremental bundle (delta) workflow.
//
// This module intentionally remains dependency-free (std lib only) so it can be
// called early from the CLI. It offers:
//   - Content-addressed cache directory derivation (PathKey, CacheDir)
//   - Snapshot load/save with atomic writes (Load, Save)
//   - Optional helpers for cache lifecycle and blob storage (Clear, SaveBlob, ReadBlob)
//
// Conventions:
//   - The cache root defaults to "tmp/.ccache" unless overridden by the caller.
//   - A per-project cache lives at: <baseTmp>/<pathKey>/
//   - The snapshot is stored at:    <baseTmp>/<pathKey>/index.json
//   - Blobs (optional) are stored under: <baseTmp>/<pathKey>/blobs/aa/bb/<sha256>
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultCacheRoot = "tmp/.ccache"
	indexFileName    = "index.json"
	blobsDirName     = "blobs"
)

// PathKey returns a short, stable identifier for an absolute project path.
// We use sha256(absPath) and keep the first 12 hex chars to avoid collisions.
func PathKey(abs string) string {
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:12]
}

// CacheDir resolves the cache directory for the given absolute source path.
// If baseTmp is empty, it falls back to the default "tmp/.ccache".
func CacheDir(baseTmp, srcAbs string) string {
	root := baseTmp
	if root == "" {
		root = defaultCacheRoot
	}
	return filepath.Join(root, PathKey(srcAbs))
}

// Load reads the snapshot from <dir>/index.json.
// If the file does not exist, it returns (nil, nil) so callers can treat it
// as "no previous snapshot" without branching on errors.
func Load(dir string) (*Snapshot, error) {
	path := filepath.Join(dir, indexFileName)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Save writes the snapshot atomically to <dir>/index.json.
// The write is performed into a temporary file within the same directory,
// then renamed to ensure readers never observe a partially-written file.
func Save(dir string, s *Snapshot) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, f, err := createTempFile(dir, indexFileName)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp) // best-effort cleanup
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	final := filepath.Join(dir, indexFileName)
	return os.Rename(tmp, final)
}

// Clear removes the entire cache directory for the project.
// Safe to call even if the directory does not exist.
func Clear(dir string) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return os.RemoveAll(dir)
}

// ----- Blob helpers (optional but useful for true deltas) -----

// SaveBlob stores content-addressed data under <dir>/blobs/aa/bb/<hash>.
// If the blob already exists, the call is a no-op.
//
// hash must be a lowercase hex string (typically sha256). The function
// validates and normalizes the storage path but does not recompute the hash.
func SaveBlob(dir, hash string, r io.Reader) error {
	if !isHex(hash) || len(hash) < 6 {
		return errors.New("invalid hash for blob storage")
	}
	blobPath := blobPath(dir, hash)
	// Fast path: if exists, skip.
	if _, err := os.Stat(blobPath); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		return err
	}
	// Atomic write
	tmp, f, err := createTempFile(filepath.Dir(blobPath), filepath.Base(blobPath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, blobPath)
}

// ReadBlob loads a blob by content hash from <dir>/blobs/aa/bb/<hash>.
func ReadBlob(dir, hash string) ([]byte, error) {
	if !isHex(hash) || len(hash) < 6 {
		return nil, errors.New("invalid hash for blob read")
	}
	blobPath := blobPath(dir, hash)
	return os.ReadFile(blobPath)
}

// HasBlob checks for the existence of a content-addressed blob.
func HasBlob(dir, hash string) bool {
	if !isHex(hash) || len(hash) < 6 {
		return false
	}
	_, err := os.Stat(blobPath(dir, hash))
	return err == nil
}

// blobPath returns the canonical path for a content-addressed blob.
// Layout: <dir>/blobs/aa/bb/<hash>
func blobPath(dir, hash string) string {
	// Normalize to lowercase hex for directory sharding.
	h := strings.ToLower(hash)
	a := h[:2]
	b := h[2:4]
	return filepath.Join(dir, blobsDirName, a, b, h)
}

// createTempFile creates a temporary file in the target directory with a
// name derived from base (".tmp-<base>-<pid>-<rand>"), returning its path
// and an *os.File ready for writing. Caller is responsible for closing it.
func createTempFile(dir, base string) (string, *os.File, error) {
	// We prefer Prefix = ".tmp-<base>-" to keep sibling entries grouped.
	prefix := ".tmp-" + base + "-"
	f, err := os.CreateTemp(dir, prefix)
	if err != nil {
		return "", nil, err
	}
	return f.Name(), f, nil
}

// isHex checks if s is a lowercase hex string.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
