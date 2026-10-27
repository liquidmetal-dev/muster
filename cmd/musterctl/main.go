// Command musterctl is the command line client for musterd.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/liquidmetal-dev/muster/internal/musterctl"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := musterctl.NewRootCmd().ExecuteContext(ctx)
	stop()
	os.Exit(musterctl.ExitCode(err))
}
