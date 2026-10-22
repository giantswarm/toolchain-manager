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
	ToolCreateToolchain   = "create_toolchain"
	ToolBuildStatus       = "toolchain_build_status"
)

const toolchainsInstructions = " list_toolchains shows the toolchains the offered sources have published, each with its versions and image; describe_toolchain shows one version's tools, Harnesses and pins as its image declares them. create_toolchain opens a pull request in a source, as you, that adds a toolchain from the Dockerfile add_packages and add_binary returned (or a hand-written one); its merge publishes the toolchain once the contract check passes. toolchain_build_status follows that pull request from queued through its checks and the release to published, or to failed with the failing job's log. All of them read and write the sources with your grant of the toolchain-manager's GitHub App in muster."

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

	s.AddTool(mcp.NewTool(ToolCreateToolchain,
		mcp.WithDescription("Writes to a repository. Open a pull request, as you, in an offered source (list_sources) that adds a new toolchain: its Dockerfile at toolchains/<name>/Dockerfile on the branch toolchain/<name>. Pass the Dockerfile add_packages or add_binary returned, or a hand-written one (advanced mode) whose final stage labels io.giantswarm.toolchain.name with the name. The source's CI runs the toolchain contract check on the pull request, and its merge publishes the image <registry>/<name>:X.Y.Z and latest; toolchain_build_status follows it. A toolchain the source already has, or one with an open pull request, is refused. Needs push access to the source. Answers status grant_required while you have not granted the toolchain-manager's GitHub App in muster."),
		mcp.WithString("dockerfile", mcp.Required(), mcp.Description("The toolchain's Dockerfile, as add_packages or add_binary returned it, or hand-written")),
		mcp.WithString("name", mcp.Description("The toolchain's name ([a-z0-9-]); default the name of a Dockerfile the manager wrote, required for a hand-written one")),
		mcp.WithString("source", mcp.Description("The source to open it in: its name or repository (owner/name), as list_sources shows it; required when more than one is offered")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	), t.create)

	s.AddTool(mcp.NewTool(ToolBuildStatus, append([]mcp.ToolOption{
		mcp.WithDescription("Read-only. The build state of a toolchain's pull request, the newest create_toolchain opened (branch toolchain/<name>) or the one pull names: queued (no CI job started), checking (its checks run, the toolchain contract among them), checked (all passed, waiting for its merge), building (merged, the release runs), published (with its version, image and release), failed (with the failing job's log), unreleased (merged, no release of it) or closed. With wait it waits up to 25 seconds for the state to move, so calling it again follows the build without polling by hand. Answers status grant_required while you have not granted the toolchain-manager's GitHub App in muster."),
		mcp.WithString("name", mcp.Required(), mcp.Description("The toolchain's name")),
		mcp.WithNumber("pull", mcp.Description("The pull request's number; default the newest from toolchain/<name>")),
		mcp.WithBoolean("wait", mcp.Description("Wait for the state to move, up to 25 seconds (default false)")),
		mcp.WithString("source", mcp.Description("The source: its name or repository (owner/name), as list_sources shows it; required when more than one is offered")),
	}, hints...)...), t.status)
}

type toolchainTools struct{ c *toolchains.Catalog }

func (t *toolchainTools) list(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	res, err := t.c.List(ctx, req.GetString("source", ""))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultStructured(res, res.Message), nil
}

func (t *toolchainTools) create(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	res, err := t.c.Create(ctx, req.GetString("source", ""), req.GetString("name", ""), req.GetString("dockerfile", ""))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultStructured(res, res.Message), nil
}

func (t *toolchainTools) status(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	res, err := t.c.BuildStatus(ctx, req.GetString("source", ""), req.GetString("name", ""), req.GetInt("pull", 0), req.GetBool("wait", false))
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
