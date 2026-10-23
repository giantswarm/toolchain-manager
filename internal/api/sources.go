package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/toolchain-manager/internal/sources"
)

// Source tool names.
const (
	ToolListSources   = "list_sources"
	ToolAddSource     = "add_source"
	ToolApproveSource = "approve_source"
	ToolRemoveSource  = "remove_source"
)

const sourcesInstructions = " The installation's toolchain sources are the repositories its configuration names and the ones people add: list_sources shows them with whether their toolchains are offered. add_source adds a GitHub repository you maintain, on which its owner installed the toolchain-manager's GitHub App (the grant), with the Slack channel of the team that owns it; the installation's owner approves a new source once (approve_source) before its toolchains are offered. remove_source removes an added one."

// addSourceTools registers the source tools.
func addSourceTools(s *mcpserver.MCPServer, m *sources.Manager) {
	t := &sourceTools{m: m}
	ref := mcp.WithString("source", mcp.Required(), mcp.Description("The source's name or repository (owner/name), as list_sources shows it"))

	s.AddTool(mcp.NewTool(ToolListSources,
		mcp.WithDescription("Read-only. List the installation's toolchain sources: those its configuration names (approved by being named there) and those people added, each with its repository, the registry its toolchains publish under, its owning team's Slack channel, who added and approved it, and whether its toolchains are offered."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	), t.list)

	s.AddTool(mcp.NewTool(ToolAddSource,
		mcp.WithDescription("Add a GitHub repository as a toolchain source. The toolchain-manager's GitHub App must be installed on it (its owner installs it once: that is the grant to read it, open pull requests and read its CI), checked with your own grant of the App in muster, and you must be able to push to it. Its CI builds and publishes its toolchains with its own credentials to its registry. A source you add waits for the installation owner's approval (approve_source) before its toolchains are offered; an owner's own source is approved by adding it. Answers status grant_required while you have not granted the App in muster, install_required while the App does not reach the repository."),
		mcp.WithString("repository", mcp.Required(), mcp.Description("The repository, owner/name or its https://github.com/ URL")),
		mcp.WithString("registry", mcp.Required(), mcp.Description("Image repository prefix the source's toolchains publish under, <registry>/<name>, such as registry.example.com/team/toolchains")),
		mcp.WithString("slackChannel", mcp.Required(), mcp.Description("Slack channel of the team that owns the source: its ID (C0123ABCDE), which the outdated notices are posted to, or its name such as #team-toolchains, which shows it only")),
		mcp.WithString("team", mcp.Description("The owning GitHub team's slug, such as team-toolchains")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	), t.add)

	s.AddTool(mcp.NewTool(ToolApproveSource,
		mcp.WithDescription("Approve an added toolchain source, as the installation's owner: its contract-checked toolchains are then offered, with no further approval. Approving an approved source changes nothing."),
		ref,
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	), t.approve)

	s.AddTool(mcp.NewTool(ToolRemoveSource,
		mcp.WithDescription("Remove an added toolchain source, as the person who added it or the installation's owner: its toolchains are no longer offered. Nothing in the repository or its registry changes. A source the installation's configuration names is removed there."),
		ref,
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	), t.remove)
}

type sourceTools struct{ m *sources.Manager }

// SourcesResult is list_sources' answer.
type SourcesResult struct {
	Sources []sources.Source `json:"sources"`
}

func (t *sourceTools) list(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	list, err := t.m.List(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	names := make([]string, len(list))
	for i, s := range list {
		names[i] = fmt.Sprintf("%s (%s, %s)", s.Repository, s.Origin, s.Phase)
	}
	return mcp.NewToolResultStructured(SourcesResult{Sources: list}, fmt.Sprintf("%d sources: %s", len(list), strings.Join(names, ", "))), nil
}

func (t *sourceTools) add(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in sources.AddInput
	if err := req.BindArguments(&in); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	res, err := t.m.Add(ctx, in)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultStructured(res, res.Message), nil
}

func (t *sourceTools) approve(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	src, err := t.m.Approve(ctx, req.GetString("source", ""))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultStructured(src, fmt.Sprintf("%s approved by %s: its contract-checked toolchains are offered", src.Repository, src.ApprovedBy)), nil
}

func (t *sourceTools) remove(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	src, err := t.m.Remove(ctx, req.GetString("source", ""))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultStructured(src, fmt.Sprintf("%s removed: its toolchains are no longer offered", src.Repository)), nil
}
