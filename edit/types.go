package edit

import "github.com/dan-strohschein/chisel/resolve"

// EditKind classifies edits for ordering and reporting.
type EditKind int

const (
	SymbolRename   EditKind = iota // Direct rename of a symbol occurrence
	ImportUpdate                    // Update an import path
	ImportAdd                       // Add a new import statement
	ImportRemove                    // Remove an import statement
	SignatureChange                 // Modify a function signature
	CallSiteUpdate                  // Update a function call
	TypeReference                   // Update a type reference
	AidUpdate                       // Update an AID file
	WholeFile                       // Whole-file replacement (used by emitter-based AID edits)
	FileCreate                      // Create a new file (used by extract command)
)

// Edit is a single text edit to apply to a source file.
type Edit struct {
	File    string   // Absolute path to the file being edited
	Line    int      // Line number where the edit starts (1-based)
	OldText string   // The exact text to find and replace on this line
	NewText string   // The replacement text
	Kind    EditKind // What type of edit this is
}

// EditSet is a complete set of edits for one refactoring operation.
type EditSet struct {
	Intent    resolve.Intent
	Edits     []Edit   // All edits, sorted by file then line descending
	FileCount int
	EditCount int
	AidEdits  []Edit   // Edits to AID files (applied after source edits)
}
