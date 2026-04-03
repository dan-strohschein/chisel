package edit

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dan-strohschein/aidkit/pkg/parser"
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

func TestExtractFunctionBody(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		startLine int
		wantLines int // number of lines in result
		wantErr   bool
	}{
		{
			name: "simple function",
			source: `package main

func hello() {
	fmt.Println("hi")
}
`,
			startLine: 3,
			wantLines: 3, // func line, body line, closing brace
		},
		{
			name: "nested braces",
			source: `package main

func process(x int) {
	if x > 0 {
		for i := 0; i < x; i++ {
			fmt.Println(i)
		}
	}
}
`,
			startLine: 3,
			wantLines: 7,
		},
		{
			name: "string literal with braces",
			source: `package main

func tricky() {
	s := "if x { y }"
	fmt.Println(s)
}
`,
			startLine: 3,
			wantLines: 4,
		},
		{
			name: "line comment with braces",
			source: `package main

func commented() {
	x := 1 // { weird }
	fmt.Println(x)
}
`,
			startLine: 3,
			wantLines: 4,
		},
		{
			name: "raw string with braces",
			source: "package main\n\nfunc rawStr() {\n\ts := `{json}`\n\tfmt.Println(s)\n}\n",
			startLine: 3,
			wantLines: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := filepath.Join(t.TempDir(), "test.go")
			if err := os.WriteFile(tmpFile, []byte(tt.source), 0644); err != nil {
				t.Fatal(err)
			}

			body, err := extractFunctionBody(tmpFile, tt.startLine)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
			if len(lines) != tt.wantLines {
				t.Errorf("expected %d lines, got %d:\n%s", tt.wantLines, len(lines), body)
			}

			// Body should start with "func" and end with "}"
			if !strings.HasPrefix(strings.TrimSpace(lines[0]), "func") {
				t.Errorf("expected body to start with func, got: %q", lines[0])
			}
			if strings.TrimSpace(lines[len(lines)-1]) != "}" {
				t.Errorf("expected body to end with }, got: %q", lines[len(lines)-1])
			}
		})
	}
}

func TestCountBracesInLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantDepth int
		wantStart bool
	}{
		{
			name:      "normal code",
			line:      "if x { y }",
			wantDepth: 0, // one open, one close
			wantStart: true,
		},
		{
			name:      "string literal braces ignored",
			line:      `s := "{ }"`,
			wantDepth: 0,
			wantStart: false,
		},
		{
			name:      "comment braces ignored",
			line:      "x := 1 // { }",
			wantDepth: 0,
			wantStart: false,
		},
		{
			name:      "raw string braces ignored",
			line:      "s := `{ }`",
			wantDepth: 0,
			wantStart: false,
		},
		{
			name:      "mixed string and comment",
			line:      `x := "{"  // }`,
			wantDepth: 0,
			wantStart: false,
		},
		{
			name:      "open brace only",
			line:      "func foo() {",
			wantDepth: 1,
			wantStart: true,
		},
		{
			name:      "close brace only",
			line:      "}",
			wantDepth: -1,
			wantStart: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			depth, started, _ := countBracesInLine(tt.line, 0, false, false)
			if depth != tt.wantDepth {
				t.Errorf("depth = %d, want %d", depth, tt.wantDepth)
			}
			if started != tt.wantStart {
				t.Errorf("started = %v, want %v", started, tt.wantStart)
			}
		})
	}
}

func TestGenerateExtractEdits(t *testing.T) {
	td := testdataDir()
	srcFile := filepath.Join(td, "src", "main.go")

	resolution := &resolve.Resolution{
		Intent: resolve.Intent{
			Kind:        resolve.Extract,
			Target:      "ProcessRequest",
			Destination: "processing",
			AidDir:      filepath.Join(td, "aidocs"),
			SourceDir:   filepath.Join(td, "src"),
		},
		Symbol: resolve.GraphNode{
			Name:       "ProcessRequest",
			Kind:       "Function",
			Module:     "testpkg",
			SourceFile: srcFile,
			SourceLine: 13,
		},
		Dependencies: []resolve.GraphNode{
			{
				Name:       "ValidateInput",
				Kind:       "Function",
				Module:     "testpkg",
				SourceFile: srcFile,
				SourceLine: 22,
			},
		},
		Locations:       []resolve.Location{},
		AffectedFiles:   []string{srcFile},
		AffectedModules: []string{"testpkg"},
	}

	edits, err := GenerateExtractEdits(resolution, "processing")
	if err != nil {
		t.Fatalf("GenerateExtractEdits failed: %v", err)
	}

	// Should have at least one FileCreate edit
	foundCreate := false
	for _, e := range edits {
		if e.Kind == FileCreate {
			foundCreate = true
			if !strings.Contains(e.NewText, "package processing") {
				t.Errorf("expected package declaration in new file, got: %s", e.NewText[:80])
			}
			break
		}
	}
	if !foundCreate {
		t.Error("expected a FileCreate edit, found none")
	}
}

func TestErrorHandlingBody(t *testing.T) {
	tests := []struct {
		name     string
		funcName string
		indent   string
		errorMap map[string]resolve.ErrorHandling
		want     string
	}{
		{
			name:     "nil map returns err",
			funcName: "Foo",
			indent:   "",
			errorMap: nil,
			want:     "\treturn err",
		},
		{
			name:     "wrap with message",
			funcName: "Fetch",
			indent:   "",
			errorMap: map[string]resolve.ErrorHandling{
				"Fetch": {Strategy: "wrap", WrapMsg: "fetching data: %w"},
			},
			want: "\treturn fmt.Errorf(\"fetching data: %w\", err)",
		},
		{
			name:     "log strategy",
			funcName: "Save",
			indent:   "",
			errorMap: map[string]resolve.ErrorHandling{
				"Save": {Strategy: "log"},
			},
			want: "\tlog.Printf(\"warning: %v\", err)",
		},
		{
			name:     "convert strategy",
			funcName: "Parse",
			indent:   "",
			errorMap: map[string]resolve.ErrorHandling{
				"Parse": {Strategy: "convert", ConvertTo: "NewParseError"},
			},
			want: "\treturn NewParseError(err)",
		},
		{
			name:     "unknown function falls back to return err",
			funcName: "Unknown",
			indent:   "",
			errorMap: map[string]resolve.ErrorHandling{
				"Other": {Strategy: "wrap", WrapMsg: "other: %w"},
			},
			want: "\treturn err",
		},
		{
			name:     "wrap without message",
			funcName: "Do",
			indent:   "\t",
			errorMap: map[string]resolve.ErrorHandling{
				"Do": {Strategy: "wrap"},
			},
			want: "\t\treturn fmt.Errorf(\"%w\", err)",
		},
		{
			name:     "convert with empty ConvertTo falls back",
			funcName: "Run",
			indent:   "",
			errorMap: map[string]resolve.ErrorHandling{
				"Run": {Strategy: "convert", ConvertTo: ""},
			},
			want: "\treturn err",
		},
		{
			name:     "unknown strategy falls back",
			funcName: "Act",
			indent:   "",
			errorMap: map[string]resolve.ErrorHandling{
				"Act": {Strategy: "custom_something"},
			},
			want: "\treturn err",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errorHandlingBody(tt.funcName, tt.indent, tt.errorMap)
			if got != tt.want {
				t.Errorf("errorHandlingBody(%q, %q, ...) =\n  %q\nwant:\n  %q", tt.funcName, tt.indent, got, tt.want)
			}
		})
	}
}

func TestRenameInAidFile(t *testing.T) {
	aidSource := `@module testpkg
@lang go

---

@fn QueryEngine.Process
@purpose Process a request
@calls QueryEngine.Validate
@related QueryEngine.Save
`

	t.Run("entry name gets renamed", func(t *testing.T) {
		af, _, err := parser.ParseString(aidSource)
		if err != nil {
			t.Fatalf("ParseString failed: %v", err)
		}

		modified := renameInAidFile(af,
			"QueryEngine.Process", "QueryEngine.Execute",
			"Process", "Execute",
			true,
		)

		if !modified {
			t.Fatal("expected renameInAidFile to return true")
		}

		// The entry name should be updated
		found := false
		for _, entry := range af.Entries {
			if entry.Name == "QueryEngine.Execute" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected entry name to be renamed to QueryEngine.Execute")
		}
	})

	t.Run("field cross-references get updated", func(t *testing.T) {
		af, _, err := parser.ParseString(aidSource)
		if err != nil {
			t.Fatalf("ParseString failed: %v", err)
		}

		modified := renameInAidFile(af,
			"QueryEngine.Validate", "QueryEngine.Check",
			"Validate", "Check",
			true,
		)

		if !modified {
			t.Fatal("expected renameInAidFile to return true")
		}

		// The @calls field should reference the new name
		for _, entry := range af.Entries {
			if callsField, ok := entry.Fields["calls"]; ok {
				if strings.Contains(callsField.InlineValue, "QueryEngine.Check") {
					return // success
				}
			}
		}
		t.Error("expected @calls field to reference QueryEngine.Check")
	})

	t.Run("method rename with qualified name", func(t *testing.T) {
		af, _, err := parser.ParseString(aidSource)
		if err != nil {
			t.Fatalf("ParseString failed: %v", err)
		}

		// Rename a method using its qualified name: QueryEngine.Save -> QueryEngine.Store
		modified := renameInAidFile(af,
			"QueryEngine.Save", "QueryEngine.Store",
			"Save", "Store",
			true,
		)

		if !modified {
			t.Fatal("expected renameInAidFile to return true")
		}

		// The @related field should reference the new qualified name
		for _, entry := range af.Entries {
			if relField, ok := entry.Fields["related"]; ok {
				if strings.Contains(relField.InlineValue, "QueryEngine.Store") {
					return // success
				}
			}
		}
		t.Error("expected @related field to reference QueryEngine.Store")
	})
}
