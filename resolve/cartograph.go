package resolve

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// GraphQuerier is the interface for querying the semantic graph.
// Production uses CLIGraphQuerier (shells out to cartograph).
// Tests can use a mock implementation.
type GraphQuerier interface {
	Query(command string, args []string, aidDir string) (*GraphResult, error)
}

// CLIGraphQuerier shells out to the cartograph binary.
type CLIGraphQuerier struct {
	BinaryPath string // Path to cartograph binary. If empty, looks on PATH.
}

// Query runs a cartograph subcommand and parses the JSON result.
func (c *CLIGraphQuerier) Query(command string, args []string, aidDir string) (*GraphResult, error) {
	binary := c.BinaryPath
	if binary == "" {
		binary = "cartograph"
	}

	cmdArgs := []string{command}
	cmdArgs = append(cmdArgs, args...)
	cmdArgs = append(cmdArgs, "--format", "json")
	if aidDir != "" {
		cmdArgs = append(cmdArgs, "--dir", aidDir)
	}

	cmd := exec.Command(binary, cmdArgs...)
	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			return nil, fmt.Errorf("cartograph %s failed: %s", command, stderr)
		}
		return nil, fmt.Errorf("cartograph %s: %w", command, err)
	}

	var result GraphResult
	if err := json.Unmarshal(stdout, &result); err != nil {
		return nil, fmt.Errorf("parsing cartograph output: %w", err)
	}
	return &result, nil
}
