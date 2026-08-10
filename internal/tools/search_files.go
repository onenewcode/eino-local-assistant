package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	defaultSearchResults = 100
	maxSearchResults     = 1000
	maxSearchLineRunes   = 2000
)

var errSearchLimitReached = errors.New("search result limit reached")

// SearchFilesInput describes a bounded workspace search.
type SearchFilesInput struct {
	Query        string `json:"query" jsonschema:"description=Regular expression to search for."`
	Path         string `json:"path,omitempty" jsonschema:"description=Optional workspace-relative file or directory to search."`
	Glob         string `json:"glob,omitempty" jsonschema:"description=Optional filepath glob matched against workspace-relative paths, for example *.go."`
	MaxResults   int    `json:"max_results,omitempty" jsonschema:"description=Maximum matches (default 100, maximum 1000)."`
	ContextLines int    `json:"context_lines,omitempty" jsonschema:"description=Lines before and after each match (default 0, maximum 5)."`
}

// SearchMatch is one line matching the requested expression.
type SearchMatch struct {
	Path          string   `json:"path"`
	Line          int      `json:"line"`
	Text          string   `json:"text"`
	Before        []string `json:"before,omitempty"`
	After         []string `json:"after,omitempty"`
	TextTruncated bool     `json:"text_truncated,omitempty"`
}

// SearchFilesOutput reports bounded search results.
type SearchFilesOutput struct {
	Query        string        `json:"query"`
	Matches      []SearchMatch `json:"matches"`
	FilesScanned int           `json:"files_scanned"`
	Truncated    bool          `json:"truncated"`
}

// NewSearchFiles creates a read-only, workspace-scoped search tool.
func NewSearchFiles(opts EditFileOptions) (tool.InvokableTool, error) {
	root, err := normalizeEditRoot(opts.WorkingDir)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(
		"search_files",
		"Search workspace files with a regular expression. Results include relative paths and line numbers; use read_file for the surrounding content. The search is bounded and skips .git.",
		func(ctx context.Context, input SearchFilesInput) (SearchFilesOutput, error) {
			return searchFiles(ctx, root, input)
		},
	)
}

func searchFiles(ctx context.Context, root string, input SearchFilesInput) (SearchFilesOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SearchFilesOutput{}, err
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return SearchFilesOutput{}, errors.New("query is required")
	}
	re, err := regexp.Compile(query)
	if err != nil {
		return SearchFilesOutput{}, fmt.Errorf("compile query: %w", err)
	}
	limit := input.MaxResults
	if limit == 0 {
		limit = defaultSearchResults
	}
	if limit < 1 || limit > maxSearchResults {
		return SearchFilesOutput{}, fmt.Errorf("max_results must be between 1 and %d", maxSearchResults)
	}
	if input.ContextLines < 0 || input.ContextLines > 5 {
		return SearchFilesOutput{}, errors.New("context_lines must be between 0 and 5")
	}
	start := root
	if strings.TrimSpace(input.Path) != "" {
		start, err = safeEditPath(root, input.Path)
		if err != nil {
			return SearchFilesOutput{}, err
		}
	}
	if info, statErr := os.Stat(start); statErr != nil {
		return SearchFilesOutput{}, fmt.Errorf("stat search path: %w", statErr)
	} else if !info.IsDir() && !info.Mode().IsRegular() {
		return SearchFilesOutput{}, errors.New("search path is not a regular file or directory")
	}
	if input.Glob != "" {
		if _, err := filepath.Match(input.Glob, "probe"); err != nil {
			return SearchFilesOutput{}, fmt.Errorf("invalid glob: %w", err)
		}
	}
	output := SearchFilesOutput{Query: query, Matches: make([]SearchMatch, 0)}
	err = filepath.WalkDir(start, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != start && entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if input.Glob != "" && !matchesSearchGlob(input.Glob, rel) {
			return nil
		}
		output.FilesScanned++
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %q: %w", rel, err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		lineNumber := 0
		before := make([]string, 0, input.ContextLines)
		active := make(map[int]int)
		for scanner.Scan() {
			lineNumber++
			line := clampSearchLine(scanner.Text())
			for index, remaining := range active {
				if remaining <= 0 {
					delete(active, index)
					continue
				}
				output.Matches[index].After = append(output.Matches[index].After, line)
				active[index] = remaining - 1
			}
			if len(output.Matches) >= limit && len(active) == 0 {
				output.Truncated = true
				break
			}
			if len(output.Matches) >= limit {
				continue
			}
			if re.MatchString(scanner.Text()) {
				text := line
				textTruncated := false
				if len([]rune(scanner.Text())) > maxSearchLineRunes {
					textTruncated = true
				}
				match := SearchMatch{Path: rel, Line: lineNumber, Text: text, TextTruncated: textTruncated}
				if input.ContextLines > 0 {
					match.Before = append([]string(nil), before...)
				}
				output.Matches = append(output.Matches, match)
				if input.ContextLines > 0 {
					active[len(output.Matches)-1] = input.ContextLines
				}
				if len(output.Matches) >= limit {
					output.Truncated = true
				}
			}
			if input.ContextLines > 0 {
				before = append(before, line)
				if len(before) > input.ContextLines {
					before = before[len(before)-input.ContextLines:]
				}
			}
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return nil
		}
		return closeErr
	})
	if errors.Is(err, errSearchLimitReached) {
		output.Truncated = true
		err = nil
	}
	if err != nil {
		return SearchFilesOutput{}, fmt.Errorf("search files: %w", err)
	}
	return output, nil
}

func clampSearchLine(line string) string {
	runes := []rune(line)
	if len(runes) <= maxSearchLineRunes {
		return line
	}
	return string(runes[:maxSearchLineRunes]) + "…"
}

func matchesSearchGlob(pattern, rel string) bool {
	matched, err := filepath.Match(filepath.FromSlash(pattern), filepath.FromSlash(rel))
	if err != nil {
		return false
	}
	if matched {
		return true
	}
	matched, _ = filepath.Match(filepath.FromSlash(pattern), filepath.Base(filepath.FromSlash(rel)))
	return matched
}
