// Command musterd is the muster daemon. It loads its configuration and
// runs the daemon until SIGTERM or SIGINT.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/daemon"
	"github.com/liquidmetal-dev/muster/internal/logging"
	"github.com/liquidmetal-dev/muster/internal/version"
)

// defaultConfigPath is where the packages install the configuration.
const defaultConfigPath = "/etc/muster/musterd.json"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run returns 0 on success, 1 on a runtime error and 2 on a flag or
// configuration error.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("musterd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	configPath := fs.String("config", defaultConfigPath, "path to the JSON configuration file")
	prunePools := fs.Bool("prune-pools", false, "delete battery pools in muster's namespace that are not configured")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		if _, err := fmt.Fprintf(stdout, "musterd %s\n", version.Get()); err != nil {
			_, _ = fmt.Fprintf(stderr, "musterd: %v\n", err)
			return 1
		}
		return 0
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "musterd: invalid configuration %s:\n", *configPath)
		for line := range strings.SplitSeq(err.Error(), "\n") {
			_, _ = fmt.Fprintf(stderr, "  %s\n", line)
		}
		return 2
	}
	logger := logging.New(stderr, cfg.Log)
	logger.Info("configuration loaded", "path", *configPath, "pools", len(cfg.Pools))

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(sig)
	err = daemon.Run(context.Background(), daemon.Options{
		Config:     cfg,
		PrunePools: *prunePools,
		Logger:     logger,
		Signals:    sig,
	})
	if err != nil {
		logger.Error("musterd stopped", "err", err)
		_, _ = fmt.Fprintf(stderr, "musterd: %v\n", err)
		return 1
	}
	return 0
}
