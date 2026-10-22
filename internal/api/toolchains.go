package api

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/toolchain-manager/internal/toolchains"
)

// Toolchain tool names.
const (
	ToolListToolchains    = "list_toolchains"
	ToolDescribeToolchain = "describe_toolchain"
)

const toolchainsInstructions = " list_toolchains shows the toolchains the offered sources have published, each with its versions and image; describe_toolchain shows one version's tools, Harnesses and pins as its image declares them. Both read the sources with your grant of the toolchain-manager's GitHub App in muster."

// addToolchainTools registers the toolchain tools.
func addToolchainTools(s *mcpserver.MCPServer, c *toolchains.Catalog) {
	t := &toolchainTools{c: c}
	source := mcp.WithString("source", mcp.Description("Only this source: its name or repository (owner/name), as list_sources shows it"))
	hints := []mcp.ToolOption{
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	}

	s.AddTool(mcp.NewTool(ToolListToolchains, append([]mcp.ToolOption{
		mcp.WithDescription("Read-only. List the toolchains the installation offers: every toolchain an offered source (list_sources) has published, which its CI does only once it keeps the toolchain contract. Each with its source, its published versions (newest first), the newest one's image <registry>/<name>:X.Y.Z, when it was published and its release. A source your sign-in does not reach is listed under unreachable. Answers status grant_required while you have not granted the toolchain-manager's GitHub App in muster."),
		source,
	}, hints...)...), t.list)

	s.AddTool(mcp.NewTool(ToolDescribeToolchain, append([]mcp.ToolOption{
		mcp.WithDescription("Read-only. Describe a published toolchain at one version, the newest by default: its image, the Harnesses it serves, each tool with its version command, the pinned packages and binaries and the egress its tools need, as the version's Dockerfile labels the image, and whether the toolchain-manager wrote that Dockerfile (add_packages and add_binary change it) or it is hand-written. Answers status grant_required while you have not granted the toolchain-manager's GitHub App in muster."),
		mcp.WithString("name", mcp.Required(), mcp.Description("The toolchain's name, as list_toolchains shows it")),
		mcp.WithString("version", mcp.Description("A published version, vX.Y.Z; default the newest (latest)")),
		source,
	}, hints...)...), t.describe)
}

type toolchainTools struct{ c *toolchains.Catalog }

func (t *toolchainTools) list(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	res, err := t.c.List(ctx, req.GetString("source", ""))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultStructured(res, res.Message), nil
}

func (t *toolchainTools) describe(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	res, err := t.c.Describe(ctx, req.GetString("name", ""), req.GetString("source", ""), req.GetString("version", ""))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultStructured(res, res.Message), nil
}
