// Package musterctl holds the commands of the musterctl CLI.
package musterctl

import "github.com/spf13/cobra"

// NewRootCmd returns the musterctl root command with all subcommands attached.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "musterctl",
		Short:        "Control plane CLI for musterd",
		SilenceUsage: true,
	}
	root.AddCommand(newVersionCmd())
	return root
}

// ExitCode maps the error of a command execution to a process exit code.
func ExitCode(err error) int {
	if err != nil {
		return 1
	}
	return 0
}
