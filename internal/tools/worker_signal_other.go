//go:build !unix

package tools

import (
	"context"
	"os"
	"os/signal"
)

func workerSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt)
}
