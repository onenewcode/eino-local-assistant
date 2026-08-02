package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/provider"
	"eino-local-assistant/internal/repl"
)

func main() {
	configPath := flag.String("config", "config.yml", "path to the YAML configuration file")
	flag.Parse()

	if err := run(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "startup error: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	model, err := provider.NewOpenAIModel(ctx, cfg.Model)
	if err != nil {
		return err
	}

	session := chat.NewSession(model, cfg.Assistant.SystemPrompt)
	runner := repl.Runner{
		Input:       os.Stdin,
		Output:      os.Stdout,
		ErrorOutput: os.Stderr,
		Session:     session,
	}

	err = runner.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
