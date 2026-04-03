package patch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dan-strohschein/aidkit/pkg/parser"
	"github.com/dan-strohschein/aidkit/pkg/validator"
	"github.com/dan-strohschein/chisel/edit"
)

// PatchOptions controls how edits are applied.
type PatchOptions struct {
	DryRun       bool   // If true, generate diff but don't modify files. Default: true.
	UpdateAid    bool   // If true, also apply AID file edits after source edits succeed.
	BackupSuffix string // If non-empty, create backup files with this suffix before editing.
	OutputFormat string // "unified" (default), "json", or "summary"
}

// DefaultOptions returns safe default options (dry-run enabled).
func DefaultOptions() PatchOptions {
	return PatchOptions{
		DryRun:       true,
		UpdateAid:    true,
		OutputFormat: "unified",
	}
}

// Patch is the result of applying or previewing an EditSet.
type Patch struct {
	FilesModified    int
	EditsApplied     int
	AidFilesModified int
	DryRun           bool
	Diff             string
	Errors           []string
	Warnings         []string // Non-fatal validation warnings (e.g., broken AID cross-references)
}

// Apply applies an EditSet to the filesystem, or previews as a diff.
func Apply(editSet *edit.EditSet, options PatchOptions) (*Patch, error) {
	result := &Patch{
		DryRun: options.DryRun,
	}

	// Group edits by file
	fileEdits := groupByFile(editSet.Edits)

	var allDiffs []string
	for file, edits := range fileEdits {
		diff, err := ApplyToFile(file, edits, options.DryRun, options.BackupSuffix)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		if diff != "" {
			allDiffs = append(allDiffs, diff)
			result.FilesModified++
			result.EditsApplied += len(edits)
		}
	}

	// Apply AID edits if source edits succeeded and UpdateAid is set
	if options.UpdateAid && len(result.Errors) == 0 && len(editSet.AidEdits) > 0 {
		aidFileEdits := groupByFile(editSet.AidEdits)
		for file, edits := range aidFileEdits {
			diff, err := ApplyToFile(file, edits, options.DryRun, options.BackupSuffix)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("AID %s: %v", file, err))
				continue
			}
			if diff != "" {
				allDiffs = append(allDiffs, diff)
				result.AidFilesModified++
			}

			// Validate the AID file after edits
			var content string
			if len(edits) == 1 && edits[0].Kind == edit.WholeFile {
				content = edits[0].NewText
			} else if !options.DryRun {
				data, err := os.ReadFile(file)
				if err == nil {
					content = string(data)
				}
			}
			if content != "" {
				validateAidContent(file, content, result)
			}
		}
	}

	result.Diff = strings.Join(allDiffs, "\n")
	return result, nil
}

// ApplyToFile applies all edits for a single file.
func ApplyToFile(file string, edits []edit.Edit, dryRun bool, backupSuffix string) (string, error) {
	// Handle FileCreate edits (new files that don't exist yet)
	if len(edits) == 1 && edits[0].Kind == edit.FileCreate {
		newContent := edits[0].NewText
		diff := fmt.Sprintf("--- /dev/null\n+++ b/%s\n@@ -0,0 +1 @@\n+%s\n", file, strings.ReplaceAll(newContent, "\n", "\n+"))
		if !dryRun {
			if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
				return "", fmt.Errorf("creating directory for %s: %w", file, err)
			}
			if err := os.WriteFile(file, []byte(newContent), 0644); err != nil {
				return "", fmt.Errorf("writing %s: %w", file, err)
			}
		}
		return diff, nil
	}

	content, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", file, err)
	}

	original := string(content)
	var modified string

	// Check for whole-file replacement (emitter-based AID edits use Line=0)
	if len(edits) == 1 && edits[0].Kind == edit.WholeFile {
		modified = edits[0].NewText
	} else {
		lines := strings.Split(original, "\n")

		// Edits are pre-sorted by line descending — apply bottom to top
		for _, e := range edits {
			if e.Line < 1 || e.Line > len(lines) {
				continue
			}
			idx := e.Line - 1
			lines[idx] = strings.Replace(lines[idx], e.OldText, e.NewText, 1)
		}

		modified = strings.Join(lines, "\n")
	}

	if original == modified {
		return "", nil
	}

	diff := GenerateDiff(file, original, modified)

	if !dryRun {
		// Create backup if requested
		if backupSuffix != "" {
			if err := os.WriteFile(file+backupSuffix, content, 0644); err != nil {
				return "", fmt.Errorf("creating backup: %w", err)
			}
		}
		if err := os.WriteFile(file, []byte(modified), 0644); err != nil {
			return "", fmt.Errorf("writing %s: %w", file, err)
		}
	}

	return diff, nil
}

// GenerateDiff generates a unified diff between original and modified content.
func GenerateDiff(file, original, modified string) string {
	origLines := strings.Split(original, "\n")
	modLines := strings.Split(modified, "\n")

	var diff strings.Builder
	diff.WriteString(fmt.Sprintf("--- a/%s\n", file))
	diff.WriteString(fmt.Sprintf("+++ b/%s\n", file))

	// Simple line-by-line diff with context
	const contextLines = 3
	var changes []change

	// Find differences using a simple LCS-like approach
	// For now, use a straightforward line comparison
	maxLen := len(origLines)
	if len(modLines) > maxLen {
		maxLen = len(modLines)
	}

	i, j := 0, 0
	for i < len(origLines) || j < len(modLines) {
		if i < len(origLines) && j < len(modLines) && origLines[i] == modLines[j] {
			changes = append(changes, change{origLine: i, modLine: j, origText: origLines[i], kind: ' '})
			i++
			j++
		} else if i < len(origLines) && (j >= len(modLines) || !containsLine(modLines[j:], origLines[i])) {
			changes = append(changes, change{origLine: i, origText: origLines[i], kind: '-'})
			i++
		} else if j < len(modLines) {
			changes = append(changes, change{modLine: j, modText: modLines[j], kind: '+'})
			j++
		}
	}

	// Group changes into hunks with context
	inHunk := false
	hunkStart := 0
	for ci, c := range changes {
		if c.kind != ' ' && !inHunk {
			inHunk = true
			hunkStart = ci - contextLines
			if hunkStart < 0 {
				hunkStart = 0
			}
			// Find hunk boundaries for the header
			origStart := 0
			modStart := 0
			for _, ch := range changes[hunkStart:ci] {
				if ch.kind == ' ' || ch.kind == '-' {
					origStart = ch.origLine + 1
				}
				if ch.kind == ' ' || ch.kind == '+' {
					modStart = ch.modLine + 1
				}
			}
			if origStart == 0 {
				origStart = c.origLine + 1
			}
			if modStart == 0 {
				modStart = c.modLine + 1
			}
			diff.WriteString(fmt.Sprintf("@@ -%d +%d @@\n", origStart, modStart))
			// Print leading context
			for _, ch := range changes[hunkStart:ci] {
				if ch.kind == ' ' {
					diff.WriteString(" " + ch.origText + "\n")
				}
			}
		}
		if inHunk {
			switch c.kind {
			case '-':
				diff.WriteString("-" + c.origText + "\n")
			case '+':
				diff.WriteString("+" + c.modText + "\n")
			case ' ':
				// Check if we should close the hunk
				endOfChanges := true
				for _, future := range changes[ci+1:] {
					if future.kind != ' ' {
						endOfChanges = false
						break
					}
				}
				if endOfChanges || (ci+1 < len(changes) && isContextGap(changes, ci, contextLines)) {
					diff.WriteString(" " + c.origText + "\n")
					inHunk = false
				} else {
					diff.WriteString(" " + c.origText + "\n")
				}
			}
		}
	}

	return diff.String()
}

// FormatPatch formats a Patch result for display.
func FormatPatch(p *Patch, format string) string {
	switch format {
	case "json":
		data, _ := json.MarshalIndent(p, "", "  ")
		return string(data)
	case "summary":
		s := fmt.Sprintf("Modified %d file(s), %d edit(s) applied", p.FilesModified, p.EditsApplied)
		if p.AidFilesModified > 0 {
			s += fmt.Sprintf(", %d AID file(s) updated", p.AidFilesModified)
		}
		if p.DryRun {
			s += " (dry-run)"
		}
		if len(p.Errors) > 0 {
			s += fmt.Sprintf("\n%d error(s):\n", len(p.Errors))
			for _, e := range p.Errors {
				s += "  - " + e + "\n"
			}
		}
		if len(p.Warnings) > 0 {
			s += fmt.Sprintf("\n%d warning(s):\n", len(p.Warnings))
			for _, w := range p.Warnings {
				s += "  - " + w + "\n"
			}
		}
		return s
	default: // "unified"
		if p.Diff == "" {
			return "No changes."
		}
		header := FormatPatch(p, "summary")
		return header + "\n\n" + p.Diff
	}
}

// validateAidContent parses and validates an AID file's content,
// appending any warnings to the patch result.
func validateAidContent(file, content string, result *Patch) {
	af, _, err := parser.ParseString(content)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("AID parse error after edit in %s: %v", file, err))
		return
	}
	issues := validator.Validate(af)
	for _, issue := range issues {
		if issue.Severity >= validator.SeverityWarning {
			msg := fmt.Sprintf("AID validation [%s] %s", file, issue.Message)
			if issue.Entry != "" {
				msg = fmt.Sprintf("AID validation [%s] %s: %s", file, issue.Entry, issue.Message)
			}
			result.Warnings = append(result.Warnings, msg)
		}
	}
}

func groupByFile(edits []edit.Edit) map[string][]edit.Edit {
	m := make(map[string][]edit.Edit)
	for _, e := range edits {
		m[e.File] = append(m[e.File], e)
	}
	return m
}

func containsLine(lines []string, target string) bool {
	limit := 5
	if len(lines) < limit {
		limit = len(lines)
	}
	for _, l := range lines[:limit] {
		if l == target {
			return true
		}
	}
	return false
}

func isContextGap(changes []change, current, contextSize int) bool {
	// Check if the next non-context change is more than contextSize away
	count := 0
	for i := current + 1; i < len(changes); i++ {
		if changes[i].kind != ' ' {
			return count > contextSize*2
		}
		count++
	}
	return true
}

type change struct {
	origLine int
	origText string
	modLine  int
	modText  string
	kind     byte
}
