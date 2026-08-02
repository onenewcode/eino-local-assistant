package repl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"eino-local-assistant/internal/chat"
)

const maxInputBytes = 1024 * 1024

// Runner connects terminal input and output to one in-memory chat session.
type Runner struct {
	Input       io.Reader
	Output      io.Writer
	ErrorOutput io.Writer
	Session     *chat.Session
}

// Run serves the REPL until /exit, EOF, context cancellation, or an input error.
func (r Runner) Run(ctx context.Context) error {
	if r.Input == nil || r.Output == nil || r.Session == nil {
		return errors.New("REPL requires input, output, and session")
	}
	if r.ErrorOutput == nil {
		r.ErrorOutput = io.Discard
	}

	scanner := bufio.NewScanner(r.Input)
	scanner.Buffer(make([]byte, 0, 64*1024), maxInputBytes)

	for {
		if ctx.Err() != nil {
			return nil
		}

		fmt.Fprint(r.Output, "you> ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read terminal input: %w", err)
			}
			fmt.Fprintln(r.Output)
			return nil
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "/exit" {
			fmt.Fprintln(r.Output, "Goodbye.")
			return nil
		}

		fmt.Fprint(r.Output, "assistant> ")
		err := r.Session.Ask(ctx, input, func(content string) error {
			_, writeErr := io.WriteString(r.Output, content)
			return writeErr
		})
		if err != nil {
			fmt.Fprintln(r.Output)
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(r.ErrorOutput, "assistant error: %v\n", err)
			continue
		}

		fmt.Fprintln(r.Output)
	}
}
