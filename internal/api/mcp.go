package api

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/toolchain-manager/internal/binaries"
	"github.com/giantswarm/toolchain-manager/internal/packages"
)

// Tool names.
const (
	ToolSearchPackages = "search_packages"
	ToolAddPackages    = "add_packages"
	ToolAddBinary      = "add_binary"
	ToolInitSource     = "init_source"
)

const instructions = "Build toolchains for coding agents. A toolchain is a named set of tools published as its own OCI image, mounted read-only into a Session at /opt/kagent/tools/<name> beside the unchanged Harness image, its bin/ on PATH. search_packages finds a package source's packages with their versions per branch and, for an exact name, the dependency closure apk installs (packages and shared libraries). add_packages and add_binary write the toolchain's Dockerfile from the picked packages and release binaries, each pinned (a package to its branch's version, a binary to its version and checksum), with a wrapper in bin/ per tool that sets its environment: pass the Dockerfile they returned to change the same toolchain again; a hand-written Dockerfile is refused, since the manager does not edit it. init_source renders the files of a new toolchain source repository: its layout, the CI that runs the toolchain contract check on every pull request and publishes each changed toolchain on a merge, optional Renovate and one example toolchain. Nothing here writes to a repository: the files are returned."

// NewMCPServer builds the MCP server of the service's tools.
func NewMCPServer(svc *Service, version string) *mcpserver.MCPServer {
	instr := instructions
	if svc.ToolchainSources != nil {
		instr += sourcesInstructions
	}
	s := mcpserver.NewMCPServer("toolchain-manager", version,
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithInstructions(instr),
	)
	t := &tools{svc: svc}

	sourceProp := mcp.WithString("source", mcp.Description("Package source, "+strings.Join(svc.Sources.Names(), " or ")+"; default "+svc.DefaultSource))
	toolchainProps := []mcp.ToolOption{
		mcp.WithString("name", mcp.Description("Toolchain name ([a-z0-9-]), the directory toolchains/<name> and the mount directory /opt/kagent/tools/<name>; required for a new toolchain")),
		mcp.WithString("dockerfile", mcp.Description("The toolchain's current Dockerfile, as add_packages or add_binary returned it; omit for a new toolchain")),
		sourceProp,
		mcp.WithString("branch", mcp.Description("Branch of the package source a new toolchain builds on; default the source's newest")),
		mcp.WithArray("harnesses", mcp.Description("Harnesses the toolchain serves (kagent, claude, codex, omp); default the source's"), mcp.WithStringItems()),
	}
	toolEntry := map[string]any{
		"type":     "object",
		"required": []string{"name", "version"},
		"properties": map[string]any{
			"name":    stringSchema("Command name, its wrapper bin/<name>"),
			"path":    stringSchema("The command's file relative to the prefix; default: found in usr/local/bin, usr/bin, bin, usr/sbin or sbin"),
			"version": stringSchema("Version command, run by /bin/sh -c"),
			"env":     map[string]any{"type": "object", "additionalProperties": stringSchema(""), "description": "Environment the wrapper sets; {prefix} is the mount directory"},
		},
	}
	hints := func(readOnly, openWorld bool) []mcp.ToolOption {
		return []mcp.ToolOption{
			mcp.WithReadOnlyHintAnnotation(readOnly),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(openWorld),
		}
	}

	s.AddTool(mcp.NewTool(ToolSearchPackages, append([]mcp.ToolOption{
		mcp.WithDescription("Read-only. Search a package source for packages whose name contains query: each with its version on every branch the source offers, the exact name first. For a package named exactly as the query, closures holds its dependency closure per branch: every package the install adds (with what pulled it in) and the shared libraries they need with their providers."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Package name or part of it, such as helm")),
		sourceProp,
	}, hints(true, true)...)...), t.searchPackages)

	s.AddTool(mcp.NewTool(ToolAddPackages, append(append([]mcp.ToolOption{
		mcp.WithDescription("Writes nothing to a repository. Add packages of a package source to a toolchain and return its Dockerfile: each package pinned to its version on the toolchain's branch, installed with its dependencies under the mount directory, a bin/ wrapper and a version command declared per tool, and the dependency closure of all its packages. Pass dockerfile to change an existing toolchain, name for a new one. Without tools, each package declares the command of its own name, else every command it provides."),
		mcp.WithArray("packages", mcp.Required(), mcp.Description("Packages, name or name=version (the branch's version; another is refused)"), mcp.WithStringItems()),
		mcp.WithArray("tools", mcp.Description("Tools to declare instead of the packages' default ones"), mcp.Items(toolEntry)),
	}, toolchainProps...), hints(true, true)...)...), t.addPackages)

	s.AddTool(mcp.NewTool(ToolAddBinary, append(append([]mcp.ToolOption{
		mcp.WithDescription("Writes nothing to a repository. Add a single binary to a toolchain -- a release asset of a GitHub release, or a URL -- pinned by version and its SHA-256 checksum per architecture (amd64, arm64), installed as usr/local/bin/<binary> under the mount directory with a bin/ wrapper, and return the toolchain's Dockerfile. An archive (.tar.gz, .tgz, .tar.xz, .zip) is unpacked and its file path, or the file named binary, installed. Asset and URL are templates of {version} (without a leading v), {tag}, {arch} (amd64, arm64) and {machine} (x86_64, aarch64). The checksum is the release asset's digest, else the given one, else the download's."),
		mcp.WithString("binary", mcp.Required(), mcp.Description("Command name the binary installs as")),
		mcp.WithString("version", mcp.Required(), mcp.Description("Release tag (v2.5.0) or, with url, the version the URL takes")),
		mcp.WithString("repository", mcp.Description("GitHub repository of the release, owner/name; or url")),
		mcp.WithString("asset", mcp.Description("With repository: the release asset's name, such as golangci-lint-{version}-linux-{arch}.tar.gz")),
		mcp.WithString("url", mcp.Description("Download URL instead of a GitHub release (https)")),
		mcp.WithObject("sha256", mcp.Description("Known SHA-256 checksums by architecture"), mcp.AdditionalProperties(stringSchema(""))),
		mcp.WithString("path", mcp.Description("The binary's file within an archive")),
		mcp.WithString("versionCommand", mcp.Description("Version command, run by /bin/sh -c; default <binary> --version")),
		mcp.WithObject("env", mcp.Description("Environment the wrapper sets; {prefix} is the mount directory"), mcp.AdditionalProperties(stringSchema(""))),
	}, toolchainProps...), hints(true, true)...)...), t.addBinary)

	s.AddTool(mcp.NewTool(ToolInitSource, append([]mcp.ToolOption{
		mcp.WithDescription("Writes nothing to a repository. Render the files of a new toolchain source repository: README.md, the toolchain contract check as a required CI workflow on every pull request, the release workflow that checks, publishes (multi-arch, its version and latest) and tags each changed toolchain on a merge, renovate.json5 when renovate is true, and one example toolchain (jq) whose Dockerfile the manager wrote."),
		mcp.WithString("repository", mcp.Required(), mcp.Description("The source's repository, owner/name")),
		mcp.WithString("registry", mcp.Required(), mcp.Description("Image repository prefix the toolchains publish under, <registry>/<name>, such as registry.example.com/team/toolchains")),
		mcp.WithBoolean("renovate", mcp.Description("Write renovate.json5 (default false)")),
	}, hints(true, false)...)...), t.initSource)

	if svc.ToolchainSources != nil {
		addSourceTools(s, svc.ToolchainSources)
	}
	return s
}

type tools struct{ svc *Service }

// stringSchema is the JSON schema of a string, described when desc is set.
func stringSchema(desc string) map[string]any {
	s := map[string]any{"type": "string"}
	if desc != "" {
		s["description"] = desc
	}
	return s
}

type targetArgs struct {
	Name       string   `json:"name"`
	Dockerfile string   `json:"dockerfile"`
	Source     string   `json:"source"`
	Branch     string   `json:"branch"`
	Harnesses  []string `json:"harnesses"`
}

func (a targetArgs) target() Target {
	return Target(a)
}

func (t *tools) searchPackages(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Query  string `json:"query"`
		Source string `json:"source"`
	}
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return result(t.svc.Search(ctx, args.Source, args.Query))
}

func (t *tools) addPackages(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		targetArgs
		Packages []string        `json:"packages"`
		Tools    []packages.Tool `json:"tools"`
	}
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return result(t.svc.AddPackages(ctx, args.target(), args.Packages, args.Tools))
}

func (t *tools) addBinary(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		targetArgs
		Binary         string            `json:"binary"`
		Version        string            `json:"version"`
		Repository     string            `json:"repository"`
		Asset          string            `json:"asset"`
		URL            string            `json:"url"`
		SHA256         map[string]string `json:"sha256"`
		Path           string            `json:"path"`
		VersionCommand string            `json:"versionCommand"`
		Env            map[string]string `json:"env"`
	}
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	br := binaries.Request{
		Name: args.Binary, Repository: args.Repository, Version: args.Version,
		Asset: args.Asset, URL: args.URL, SHA256: args.SHA256, Path: args.Path,
	}
	return result(t.svc.AddBinary(ctx, args.target(), br, packages.Tool{Version: args.VersionCommand, Env: args.Env}))
}

func (t *tools) initSource(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Repository string `json:"repository"`
		Registry   string `json:"registry"`
		Renovate   bool   `json:"renovate"`
	}
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	files, err := t.svc.InitSource(ctx, args.Repository, args.Registry, args.Renovate)
	return result(map[string]any{"repository": args.Repository, "files": files}, err)
}

// result is a tool's JSON answer, or its error as a tool error.
func result[T any](v T, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(b)), nil
}
