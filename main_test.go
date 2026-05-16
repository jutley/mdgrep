package main

import (
	"os"
	"path/filepath"
	"testing"
)

// parseFrontmatter

func TestParseFrontmatter_Scalars(t *testing.T) {
	t.Parallel()
	fm := parseFrontmatter([]string{
		"title: My Doc",
		`version: "3.2.1"`,
		"count: 42",
		"ratio: 1.5",
		"active: true",
		"draft: false",
	})

	if got := fm["title"]; got != "My Doc" {
		t.Errorf("title: got %q", got)
	}
	if got := fm["version"]; got != "3.2.1" {
		t.Errorf("version: got %q (should stay string)", got)
	}
	if got := fm["count"]; got != int64(42) {
		t.Errorf("count: got %v", got)
	}
	if got := fm["ratio"]; got != float64(1.5) {
		t.Errorf("ratio: got %v", got)
	}
	if got := fm["active"]; got != true {
		t.Errorf("active: got %v", got)
	}
	if got := fm["draft"]; got != false {
		t.Errorf("draft: got %v", got)
	}
}

func TestParseFrontmatter_InlineArray(t *testing.T) {
	t.Parallel()
	fm := parseFrontmatter([]string{"tags: [go, cli, search]"})
	tags, ok := fm["tags"].([]string)
	if !ok {
		t.Fatalf("tags: want []string, got %T", fm["tags"])
	}
	if len(tags) != 3 || tags[0] != "go" || tags[1] != "cli" || tags[2] != "search" {
		t.Errorf("tags: got %v", tags)
	}
}

func TestParseFrontmatter_BlockArray(t *testing.T) {
	t.Parallel()
	fm := parseFrontmatter([]string{
		"tags:",
		"- go",
		"- cli",
		"- search",
	})
	tags, ok := fm["tags"].([]string)
	if !ok {
		t.Fatalf("tags: want []string, got %T", fm["tags"])
	}
	if len(tags) != 3 || tags[0] != "go" || tags[1] != "cli" || tags[2] != "search" {
		t.Errorf("tags: got %v", tags)
	}
}

func TestParseFrontmatter_Empty(t *testing.T) {
	t.Parallel()
	fm := parseFrontmatter(nil)
	if len(fm) != 0 {
		t.Errorf("expected empty map, got %v", fm)
	}
}

// parseHeading

func TestParseHeading(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line      string
		wantLevel int
		wantTitle string
	}{
		{"# Hello", 1, "Hello"},
		{"## World", 2, "World"},
		{"### Deep", 3, "Deep"},
		{"#NoSpace", 0, ""},
		{"####", 0, ""},
		{"not a heading", 0, ""},
		{"# ", 1, ""},
		{"##   Trimmed  ", 2, "Trimmed"},
	}
	for _, c := range cases {
		level, title := parseHeading(c.line)
		if level != c.wantLevel || title != c.wantTitle {
			t.Errorf("parseHeading(%q): got (%d, %q), want (%d, %q)",
				c.line, level, title, c.wantLevel, c.wantTitle)
		}
	}
}

// expandCombinedFlags

func TestExpandCombinedFlags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"-Rci"}, []string{"-R", "-c", "-i"}},
		{[]string{"-m5"}, []string{"-m", "5"}},
		{[]string{"-Rcm5"}, []string{"-R", "-c", "-m", "5"}},
		{[]string{"-r"}, []string{"-r"}},
		{[]string{"--filter", "x"}, []string{"--filter", "x"}},
		{[]string{"-unknown"}, []string{"-unknown"}},
		{[]string{"-r", "pattern"}, []string{"-r", "pattern"}},
	}
	for _, c := range cases {
		got := expandCombinedFlags(c.in)
		if !sliceEqual(got, c.want) {
			t.Errorf("expandCombinedFlags(%v): got %v, want %v", c.in, got, c.want)
		}
	}
}

// fmDisplay

func TestFmDisplay(t *testing.T) {
	t.Parallel()
	fm := map[string]any{
		"title":  "My Title",
		"tags":   []string{"a", "b"},
		"empty":  "",
		"number": int64(7),
	}

	if val, ok := fmDisplay(fm, "title"); !ok || val != "My Title" {
		t.Errorf("title: got (%q, %v)", val, ok)
	}
	if val, ok := fmDisplay(fm, "tags"); !ok || val != "a, b" {
		t.Errorf("tags: got (%q, %v)", val, ok)
	}
	if _, ok := fmDisplay(fm, "empty"); ok {
		t.Errorf("empty string should return ok=false")
	}
	if _, ok := fmDisplay(fm, "missing"); ok {
		t.Errorf("missing key should return ok=false")
	}
	if val, ok := fmDisplay(fm, "number"); !ok || val != "7" {
		t.Errorf("number: got (%q, %v)", val, ok)
	}
}

// sliceEqual

func TestSliceEqual(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b  []string
		equal bool
	}{
		{[]string{"a", "b"}, []string{"a", "b"}, true},
		{[]string{"a"}, []string{"a", "b"}, false},
		{[]string{"a", "b"}, []string{"a", "c"}, false},
		{nil, nil, true},
	}
	for _, c := range cases {
		if got := sliceEqual(c.a, c.b); got != c.equal {
			t.Errorf("sliceEqual(%v, %v): got %v, want %v", c.a, c.b, got, c.equal)
		}
	}
}

// scanForIndex helpers

func writeTempMD(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// scanForIndex

func TestScanForIndex_FrontmatterTitle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeTempMD(t, dir, "doc.md", `---
title: My Document
description: A great doc
---
# Some Other Heading
`)
	e, skip, err := scanForIndex(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Fatal("expected not skipped")
	}
	if e.title != "My Document" {
		t.Errorf("title: got %q", e.title)
	}
	if e.desc != "A great doc" {
		t.Errorf("desc: got %q", e.desc)
	}
}

func TestScanForIndex_SoleH1(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeTempMD(t, dir, "doc.md", "# Only Heading\n\nSome content.\n")
	e, skip, err := scanForIndex(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Fatal("expected not skipped")
	}
	if e.title != "Only Heading" {
		t.Errorf("title: got %q, want %q", e.title, "Only Heading")
	}
}

func TestScanForIndex_MultipleH1FallsBackToFilename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeTempMD(t, dir, "my-doc.md", "# First\n\n# Second\n")
	e, skip, err := scanForIndex(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Fatal("expected not skipped")
	}
	if e.title != "my-doc" {
		t.Errorf("title: got %q, want %q", e.title, "my-doc")
	}
}

func TestScanForIndex_CELFilter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeTempMD(t, dir, "doc.md", "---\ntags: [go, cli]\n---\n# Hello\n")

	match, err := compileCEL(`"go" in fm.tags`)
	if err != nil {
		t.Fatal(err)
	}
	noMatch, err := compileCEL(`"python" in fm.tags`)
	if err != nil {
		t.Fatal(err)
	}

	_, skip, err := scanForIndex(path, match)
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Error("expected file to pass filter")
	}

	_, skip, err = scanForIndex(path, noMatch)
	if err != nil {
		t.Fatal(err)
	}
	if !skip {
		t.Error("expected file to be filtered out")
	}
}
