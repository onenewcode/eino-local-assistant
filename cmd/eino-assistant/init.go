package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const defaultAgentInstructions = `# AGENTS.md

## Working Agreement

- Read existing project instructions and relevant code before making changes.
- Keep changes focused, explain important tradeoffs, and preserve user changes.
- Do not expose secrets or modify files outside the requested project scope.

## Verification

- Run the relevant formatter, tests, and build checks before handing off changes.
`

func newInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init [path]",
		Short: "Create a project instruction file",
		Long:  "Create an AGENTS.md project instruction file without overwriting an existing file. The optional path defaults to AGENTS.md in the current directory.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "AGENTS.md"
			if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
				path = args[0]
			}
			created, err := initProject(path)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Initialized %s\n", created)
			return err
		},
	}
}

func initProject(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "AGENTS.md"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve instruction path: %w", err)
	}
	info, err := os.Stat(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("inspect instruction directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("instruction file parent is not a directory")
	}
	file, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%s already exists; refusing to overwrite", path)
		}
		return "", fmt.Errorf("create instruction file: %w", err)
	}
	completed := false
	defer func() {
		if !completed {
			_ = os.Remove(abs)
		}
	}()
	if _, err := io.WriteString(file, defaultAgentInstructions); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write instruction file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close instruction file: %w", err)
	}
	completed = true
	return abs, nil
}
