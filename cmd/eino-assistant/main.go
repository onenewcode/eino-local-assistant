package main

import (
	"errors"
	"fmt"
	"os"

	"eino-local-assistant/internal/tools"
)

const (
	exitCodeSuccess = 0
	exitCodeFailure = 1
	exitCodeSIGTERM = 143
)

func exitCodeForError(err error) int {
	if err == nil {
		return exitCodeSuccess
	}
	var sigtermErr *execSIGTERMError
	if errors.As(err, &sigtermErr) {
		return exitCodeSIGTERM
	}
	return exitCodeFailure
}

func main() {
	// The parent tool runner starts this private one-shot worker inside the
	// platform sandbox. Keep it outside Cobra so it never loads user config or
	// requires a TTY.
	if len(os.Args) == 2 && os.Args[1] == "__sandbox_worker" {
		if err := tools.RunSandboxWorker(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "sandbox worker error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := execute(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitCodeForError(err))
	}
}
