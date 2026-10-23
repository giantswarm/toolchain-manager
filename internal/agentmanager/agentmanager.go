// Package agentmanager asks agent-manager, through muster's MCP endpoint and
// as the person, who uses a toolchain: its toolchain_users, the agents whose
// default toolchains name it and the Sessions that hold its digest. Every
// call carries the person's Dex ID token as `Authorization: Bearer`, so
// muster sees the person and agent-manager reads as them.
package agentmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/toolchain-manager/internal/toolchains"
)

const (
	// toolToolchainUsers is agent-manager's tool, by its own name.
	toolToolchainUsers = "toolchain_users"
	// metaCallTool is muster's meta-tool that runs any aggregated tool; the
	// target's result comes back serialised in its text content.
	metaCallTool = "call_tool"

	callTimeout = 45 * time.Second
)

// Client calls agent-manager's tools through muster, named by the configured
// prefix (x_agent-manager_) joined with the tool's own name.
type Client struct {
	endpoint string
	prefix   string
	version  string
}

// New is the client of agent-manager behind the muster MCP endpoint
// (http(s)://host[:port]/mcp) under the tool prefix. version names the
// toolchain-manager in the MCP handshake.
func New(endpoint, prefix, version string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("muster MCP URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("muster MCP URL %q: want http(s)://host[:port]/path without a query", endpoint)
	}
	if !strings.HasPrefix(prefix, "x_") || !strings.HasSuffix(prefix, "_") || len(prefix) < 4 {
		return nil, fmt.Errorf("agent-manager tool prefix %q: want x_<server>_", prefix)
	}
	return &Client{endpoint: endpoint, prefix: prefix, version: version}, nil
}

// Users asks who uses toolchain name, or its version when set.
func (c *Client) Users(ctx context.Context, bearer, name, version string) (toolchains.Usage, error) {
	args := map[string]any{"name": name}
	if version != "" {
		args["version"] = version
	}
	tool := c.prefix + toolToolchainUsers
	raw, err := c.call(ctx, bearer, tool, args)
	if err != nil {
		return toolchains.Usage{}, err
	}
	var out toolchains.Usage
	if err := json.Unmarshal(raw, &out); err != nil {
		return toolchains.Usage{}, fmt.Errorf("%s: malformed answer: %w", tool, err)
	}
	return out, nil
}

// call runs tool through muster's call_tool as the person, one MCP session
// per call, and returns its structured result.
func (c *Client) call(ctx context.Context, bearer, tool string, args map[string]any) (json.RawMessage, error) {
	if bearer == "" {
		return nil, errors.New("muster: the call carries no Dex ID token of the person")
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	client, err := mcpclient.NewStreamableHttpClient(c.endpoint,
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + bearer}))
	if err != nil {
		return nil, fmt.Errorf("muster MCP client: %w", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("muster: %w", err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "toolchain-manager", Version: c.version}
	if _, err := client.Initialize(ctx, init); err != nil {
		return nil, fmt.Errorf("muster: initialize: %w", err)
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = metaCallTool
	req.Params.Arguments = map[string]any{"name": tool, "arguments": args}
	outer, err := client.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("muster: call %s: %w", tool, err)
	}
	inner, dispatched := unwrap(outer)
	if inner.IsError {
		if !dispatched {
			return nil, fmt.Errorf("muster did not run %s: %s", tool, textOf(inner))
		}
		return nil, fmt.Errorf("%s: %s", tool, textOf(inner))
	}
	raw := structuredOf(inner)
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s: malformed answer: no structured result", tool)
	}
	return raw, nil
}

// unwrap reads the target tool's own result out of call_tool's text. A
// result that is no envelope is call_tool's own refusal (an unknown tool,
// one outside the person's toolset): not dispatched.
func unwrap(outer *mcp.CallToolResult) (*mcp.CallToolResult, bool) {
	var env mcp.CallToolResult
	if err := json.Unmarshal([]byte(textOf(outer)), &env); err != nil || len(env.Content) == 0 {
		return outer, false
	}
	return &env, true
}

func textOf(r *mcp.CallToolResult) string {
	var texts []string
	for _, c := range r.Content {
		if t, ok := mcp.AsTextContent(c); ok && t.Text != "" {
			texts = append(texts, t.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func structuredOf(r *mcp.CallToolResult) json.RawMessage {
	if len(r.RawStructuredContent) > 0 && string(r.RawStructuredContent) != "null" {
		return r.RawStructuredContent
	}
	if r.StructuredContent == nil {
		return nil
	}
	raw, err := json.Marshal(r.StructuredContent)
	if err != nil {
		return nil
	}
	return raw
}
