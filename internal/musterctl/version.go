package musterctl

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/liquidmetal-dev/muster/internal/version"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the musterctl version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "musterctl %s\n", version.Get())
			return err
		},
	}
}
