package main

import (
	"reflect"
	"testing"
)

func TestParseFlagsBasic(t *testing.T) {
	args := []string{"-zip", "out.zip", "-diff-context", "7", "-diff-no-prefix=false", "-bench", "bench.txt", "."}
	cfg, err := parseFlags(args)
	if err != nil {
		t.Fatalf("parseFlags error: %v", err)
	}
	if cfg.zipOut != "out.zip" {
		t.Fatalf("zipOut got %q", cfg.zipOut)
	}
	if cfg.diffContext != 7 {
		t.Fatalf("diffContext got %d", cfg.diffContext)
	}
	if cfg.diffNoPrefix != false {
		t.Fatalf("diffNoPrefix got %v", cfg.diffNoPrefix)
	}
	if cfg.benchPath != "bench.txt" {
		t.Fatalf("benchPath got %q", cfg.benchPath)
	}
}

func TestParseFlagsMissingSrcDir(t *testing.T) {
	args := []string{"-zip", "out.zip"}
	if _, err := parseFlags(args); err == nil {
		t.Fatalf("expected error for missing <src_dir>")
	}
}

func TestParseFlagsExtWithSpaces(t *testing.T) {
	args := []string{"-zip", "out.zip", "-ext", ".go, .java , .py", "."}
	cfg, err := parseFlags(args)
	if err != nil {
		t.Fatalf("parseFlags with spaced -ext error: %v", err)
	}
	if cfg.exts == "" {
		t.Fatalf("exts should be captured, got empty")
	}
}

func TestBuildOptionsAndLangs(t *testing.T) {
	cfg := Config{maxDiffBytes: 123, diffContext: 5, diffNoPrefix: true}
	opt, langs, err := buildOptions(cfg)
	if err != nil {
		t.Fatalf("buildOptions error: %v", err)
	}
	if opt.MaxBytes != 123 || opt.Context != 5 || !opt.NoPrefix {
		t.Fatalf("unexpected options: %+v", opt)
	}
	want := []string{"cpp", "cs", "go", "java", "kt", "py", "ts", "tsx"}
	if !reflect.DeepEqual(langs, want) {
		t.Fatalf("langs mismatch: got %v want %v", langs, want)
	}
}

func TestSelectMode(t *testing.T) {
	if m, _ := selectMode(Config{zipOut: "a"}); m != "full" {
		t.Fatalf("mode=%s", m)
	}
	if m, _ := selectMode(Config{deltaOut: "b"}); m != "delta" {
		t.Fatalf("mode=%s", m)
	}
	if m, _ := selectMode(Config{chatOut: "c"}); m != "chat" {
		t.Fatalf("mode=%s", m)
	}
	if _, err := selectMode(Config{zipOut: "a", deltaOut: "b"}); err == nil {
		t.Fatalf("expected error on conflicting modes")
	}
}

func TestSelectModeNoMode(t *testing.T) {
	if _, err := selectMode(Config{}); err == nil {
		t.Fatalf("expected error when no mode is selected")
	}
}
