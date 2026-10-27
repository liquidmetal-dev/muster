// Command musterd is the muster daemon. It is a placeholder: it prints its
// version and exits until later issues add the daemon itself.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/liquidmetal-dev/muster/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("musterd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
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
	fs.Usage()
	return 2
}
