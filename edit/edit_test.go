package edit

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dan-strohschein/chisel/resolve"
)

func testdataDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata")
}

func TestScopeMatch(t *testing.T) {
	tests := []struct {
		line            string
		symbol          string
		lang            string
		includeComments bool
		want            bool
	}{
		// Real code references
		{"func NewQueryEngine() *QueryEngine {", "QueryEngine", "go", false, true},
		{"	engine := QueryEngine{}", "QueryEngine", "go", false, true},
		{"	qe.QueryEngine.Run()", "QueryEngine", "go", false, true},

		// Inside string literal (always excluded)
		{`	fmt.Println("QueryEngine started")`, "QueryEngine", "go", false, false},
		{`	fmt.Println("QueryEngine started")`, "QueryEngine", "go", true, false},

		// Inside comment (excluded by default, included with flag)
		{"	// QueryEngine handles queries", "QueryEngine", "go", false, false},
		{"	// QueryEngine handles queries", "QueryEngine", "go", true, true},
		{"	# QueryEngine handles queries", "QueryEngine", "python", false, false},
		{"	# QueryEngine handles queries", "QueryEngine", "python", true, true},

		// Inside backtick string (always excluded)
		{"	msg := `QueryEngine error`", "QueryEngine", "go", false, false},

		// Not present at all
		{"	foo := bar()", "QueryEngine", "go", false, false},
	}

	for _, tt := range tests {
		got := ScopeMatch(tt.line, tt.symbol, tt.lang, tt.includeComments)
		if got != tt.want {
			t.Errorf("ScopeMatch(%q, %q, %q, includeComments=%v) = %v, want %v",
				tt.line, tt.symbol, tt.lang, tt.includeComments, got, tt.want)
		}
	}
}

func TestGenerateRenameEdits(t *testing.T) {
	td := testdataDir()
	srcFile := filepath.Join(td, "src", "main.go")

	resolution := &resolve.Resolution{
		Intent: resolve.Intent{
			Kind:      resolve.Rename,
			Target:    "QueryEngine",
			NewName:   "GraphQueryEngine",
			AidDir:    filepath.Join(td, "aidocs"),
			SourceDir: filepath.Join(td, "src"),
		},
		Symbol: resolve.GraphNode{
			Name: "QueryEngine",
			Kind: "Type",
		},
		Locations: []resolve.Location{
			{File: srcFile, Line: 5, SymbolKind: "definition", Context: "type QueryEngine struct {"},
			{File: srcFile, Line: 9, SymbolKind: "type_ref", Context: "func NewQueryEngine(depth int) *QueryEngine {"},
			{File: srcFile, Line: 10, SymbolKind: "call", Context: "	return &QueryEngine{maxDepth: depth}"},
		},
		AffectedFiles:   []string{srcFile},
		AffectedModules: []string{"testpkg"},
	}

	edits, err := GenerateRenameEdits(resolution, "GraphQueryEngine", nil)
	if err != nil {
		t.Fatalf("GenerateRenameEdits failed: %v", err)
	}

	if len(edits) != 3 {
		t.Fatalf("expected 3 edits, got %d", len(edits))
	}

	for _, e := range edits {
		if e.OldText != "QueryEngine" {
			t.Errorf("expected OldText 'QueryEngine', got %q", e.OldText)
		}
		if e.NewText != "GraphQueryEngine" {
			t.Errorf("expected NewText 'GraphQueryEngine', got %q", e.NewText)
		}
	}
}

func TestGenerateEditsFullPipeline(t *testing.T) {
	td := testdataDir()
	srcFile := filepath.Join(td, "src", "main.go")

	resolution := &resolve.Resolution{
		Intent: resolve.Intent{
			Kind:      resolve.Rename,
			Target:    "QueryEngine",
			NewName:   "GraphQueryEngine",
			AidDir:    filepath.Join(td, "aidocs"),
			SourceDir: filepath.Join(td, "src"),
		},
		Symbol: resolve.GraphNode{
			Name: "QueryEngine",
			Kind: "Type",
		},
		Locations: []resolve.Location{
			{File: srcFile, Line: 5, SymbolKind: "definition", Context: "type QueryEngine struct {"},
			{File: srcFile, Line: 10, SymbolKind: "call", Context: "	return &QueryEngine{maxDepth: depth}"},
		},
		AffectedFiles:   []string{srcFile},
		AffectedModules: []string{"testpkg"},
	}

	editSet, err := GenerateEdits(resolution, nil)
	if err != nil {
		t.Fatalf("GenerateEdits failed: %v", err)
	}

	if editSet.EditCount == 0 {
		t.Error("expected at least one edit")
	}

	// Verify edits are sorted by file ascending, line descending
	for i := 1; i < len(editSet.Edits); i++ {
		prev := editSet.Edits[i-1]
		curr := editSet.Edits[i]
		if prev.File == curr.File && prev.Line < curr.Line {
			t.Errorf("edits not sorted descending within file: line %d before %d", prev.Line, curr.Line)
		}
	}
}

func TestReadLineFromFile(t *testing.T) {
	td := testdataDir()
	srcFile := filepath.Join(td, "src", "main.go")

	// Verify the fixture file exists
	if _, err := os.Stat(srcFile); os.IsNotExist(err) {
		t.Skipf("test fixture not found: %s", srcFile)
	}

	line, err := ReadLineFromFile(srcFile, 5)
	if err != nil {
		t.Fatalf("ReadLineFromFile failed: %v", err)
	}
	if line != "type QueryEngine struct {" {
		t.Errorf("unexpected line content: %q", line)
	}

	// Test out of range
	_, err = ReadLineFromFile(srcFile, 9999)
	if err == nil {
		t.Error("expected error for out-of-range line")
	}
}
