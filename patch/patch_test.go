package patch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dan-strohschein/chisel/edit"
)

func TestApplyToFileDryRun(t *testing.T) {
	// Create a temp file
	dir := t.TempDir()
	file := filepath.Join(dir, "test.go")
	content := `package main

type QueryEngine struct {
	maxDepth int
}

func NewQueryEngine() *QueryEngine {
	return &QueryEngine{}
}
`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	edits := []edit.Edit{
		{File: file, Line: 7, OldText: "QueryEngine", NewText: "GraphQueryEngine", Kind: edit.SymbolRename},
		{File: file, Line: 3, OldText: "QueryEngine", NewText: "GraphQueryEngine", Kind: edit.SymbolRename},
	}

	// Sort descending (as the edit package would)
	// Line 7 before line 3

	diff, err := ApplyToFile(file, edits, true, "")
	if err != nil {
		t.Fatalf("ApplyToFile failed: %v", err)
	}

	if diff == "" {
		t.Fatal("expected non-empty diff")
	}

	// Verify file was NOT modified (dry-run)
	actual, _ := os.ReadFile(file)
	if string(actual) != content {
		t.Error("dry-run modified the file")
	}

	// Verify diff contains the changes
	if !strings.Contains(diff, "GraphQueryEngine") {
		t.Error("diff should contain the new name")
	}
}

func TestApplyToFileWithBackup(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.go")
	content := "type QueryEngine struct {}\n"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	edits := []edit.Edit{
		{File: file, Line: 1, OldText: "QueryEngine", NewText: "GraphQueryEngine", Kind: edit.SymbolRename},
	}

	_, err := ApplyToFile(file, edits, false, ".bak")
	if err != nil {
		t.Fatalf("ApplyToFile failed: %v", err)
	}

	// Verify backup exists
	backup, err := os.ReadFile(file + ".bak")
	if err != nil {
		t.Fatal("backup file not created")
	}
	if string(backup) != content {
		t.Error("backup content doesn't match original")
	}

	// Verify file was modified
	modified, _ := os.ReadFile(file)
	if !strings.Contains(string(modified), "GraphQueryEngine") {
		t.Error("file should contain the new name")
	}
}

func TestGenerateDiff(t *testing.T) {
	original := "line 1\nline 2\nline 3\n"
	modified := "line 1\nline TWO\nline 3\n"

	diff := GenerateDiff("test.go", original, modified)

	if !strings.Contains(diff, "--- a/test.go") {
		t.Error("diff missing --- header")
	}
	if !strings.Contains(diff, "+++ b/test.go") {
		t.Error("diff missing +++ header")
	}
	if !strings.Contains(diff, "-line 2") {
		t.Error("diff missing removed line")
	}
	if !strings.Contains(diff, "+line TWO") {
		t.Error("diff missing added line")
	}
}

func TestFormatPatchSummary(t *testing.T) {
	p := &Patch{
		FilesModified: 3,
		EditsApplied:  10,
		DryRun:        true,
	}

	out := FormatPatch(p, "summary")
	if !strings.Contains(out, "3 file(s)") {
		t.Errorf("summary missing file count: %s", out)
	}
	if !strings.Contains(out, "10 edit(s)") {
		t.Errorf("summary missing edit count: %s", out)
	}
	if !strings.Contains(out, "dry-run") {
		t.Errorf("summary missing dry-run indicator: %s", out)
	}
}

func TestFormatPatchJSON(t *testing.T) {
	p := &Patch{
		FilesModified: 1,
		EditsApplied:  2,
		DryRun:        true,
		Diff:          "some diff",
	}

	out := FormatPatch(p, "json")
	if !strings.Contains(out, `"FilesModified"`) {
		t.Errorf("json missing FilesModified: %s", out)
	}
}

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()
	if !opts.DryRun {
		t.Error("DryRun should default to true")
	}
	if !opts.UpdateAid {
		t.Error("UpdateAid should default to true")
	}
	if opts.OutputFormat != "unified" {
		t.Errorf("OutputFormat should default to unified, got %s", opts.OutputFormat)
	}
}

func TestApplyFullEditSet(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.go")
	content := "type QueryEngine struct {\n\tmaxDepth int\n}\n\nfunc NewQueryEngine() *QueryEngine {\n\treturn &QueryEngine{}\n}\n"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	editSet := &edit.EditSet{
		Edits: []edit.Edit{
			{File: file, Line: 5, OldText: "QueryEngine", NewText: "GraphQueryEngine", Kind: edit.SymbolRename},
			{File: file, Line: 1, OldText: "QueryEngine", NewText: "GraphQueryEngine", Kind: edit.SymbolRename},
		},
		FileCount: 1,
		EditCount: 2,
	}

	result, err := Apply(editSet, DefaultOptions())
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if result.FilesModified != 1 {
		t.Errorf("expected 1 file modified, got %d", result.FilesModified)
	}
	if !result.DryRun {
		t.Error("should be dry-run")
	}

	// Verify file NOT modified (dry-run)
	actual, _ := os.ReadFile(file)
	if string(actual) != content {
		t.Error("dry-run should not modify files")
	}
}
