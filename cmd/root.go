// Package cmd is the toolchain-manager's command line.
package cmd

import (
	"github.com/spf13/cobra"
)

// version is stamped at build time.
var version = "dev"

// NewRootCommand returns the toolchain-manager command.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "toolchain-manager",
		Short:         "MCP server that manages toolchains for coding agents",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newServeCommand())
	return root
}
