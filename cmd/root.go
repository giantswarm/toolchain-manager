// Package cmd is the toolchain-manager's command line.
package cmd

import (
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// version is the release the binary was built from: -ldflags -X when a
// build sets it, else the main module's version the Go toolchain recorded
// from the tag at HEAD (the release pipeline sets no -X).
var version = "dev"

func init() {
	if version != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = strings.TrimPrefix(bi.Main.Version, "v")
	}
}

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
