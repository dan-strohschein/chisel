package resolve

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dan-strohschein/aidkit/pkg/discovery"
	"github.com/dan-strohschein/aidkit/pkg/l2"
	"github.com/dan-strohschein/aidkit/pkg/parser"
)

// StaleReport contains staleness information for a single AID file.
type StaleReport struct {
	AidFile string          `json:"aid_file"`
	Module  string          `json:"module"`
	Claims  []l2.StaleClaim `json:"claims"`
}

// CheckAllStaleness discovers AID files and checks each for stale claims.
// Returns reports only for files that have stale claims.
func CheckAllStaleness(startDir string) ([]StaleReport, error) {
	result, err := discovery.Discover(startDir)
	if err != nil {
		return nil, fmt.Errorf("discovering .aidocs/: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("no .aidocs/ directory found")
	}

	projectRoot := filepath.Dir(result.AidDocsPath)
	var reports []StaleReport

	for _, aidFileName := range result.AidFiles {
		aidPath := filepath.Join(result.AidDocsPath, aidFileName)
		af, _, err := parser.ParseFile(aidPath)
		if err != nil {
			continue
		}

		// Skip files without @code_version (can't check staleness)
		if af.Header.CodeVersion == "" {
			continue
		}

		claims, err := l2.CheckStaleness(af, projectRoot)
		if err != nil {
			continue
		}

		if len(claims) > 0 {
			reports = append(reports, StaleReport{
				AidFile: aidPath,
				Module:  af.Header.Module,
				Claims:  claims,
			})
		}
	}

	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Module < reports[j].Module
	})

	return reports, nil
}

// FormatStaleReports formats staleness reports as human-readable text.
func FormatStaleReports(reports []StaleReport) string {
	if len(reports) == 0 {
		return "All AID files are up to date.\n"
	}

	var sb strings.Builder
	totalClaims := 0
	for _, r := range reports {
		totalClaims += len(r.Claims)
	}
	fmt.Fprintf(&sb, "Found %d stale claim(s) across %d AID file(s):\n\n", totalClaims, len(reports))

	for _, report := range reports {
		fmt.Fprintf(&sb, "%s (%s):\n", report.Module, filepath.Base(report.AidFile))
		for _, claim := range report.Claims {
			loc := ""
			if claim.Ref.File != "" {
				loc = fmt.Sprintf("%s:%d", claim.Ref.File, claim.Ref.StartLine)
			}
			fmt.Fprintf(&sb, "  %s.%s: %s", claim.Entry, claim.Field, claim.Reason)
			if loc != "" {
				fmt.Fprintf(&sb, " at %s", loc)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
