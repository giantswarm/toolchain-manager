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
	"sync"
	"time"

	"github.com/giantswarm/mcp-toolkit/urlelicitation"
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

// call runs a tool through muster and returns its JSON result. URL input
// requests are recorded for the relay and retain the producer's message.
func (c *Client) call(ctx context.Context, bearer, tool string, args map[string]any) (json.RawMessage, error) {
	if bearer == "" {
		return nil, errors.New("muster: the call carries no Dex ID token of the person")
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	t, err := transport.NewStreamableHTTP(c.endpoint,
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + bearer}))
	if err != nil {
		return nil, fmt.Errorf("muster MCP client: %w", err)
	}
	// A server/discover slower than mcp-go's probe bound would fall back to
	// the 2025-11-25 handshake, which carries no InputRequiredResult; the
	// call's own deadline bounds it.
	client := mcpclient.NewClient(t, mcpclient.WithDiscoverTimeout(-1), mcpclient.WithManualInputRoundTrips())
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
	relay := relayFrom(ctx)
	target, err := urlelicitation.RequestBinding(req)
	if err != nil {
		return nil, err
	}
	if relay != nil {
		ctx = mcpclient.WithRequestClientCapabilities(ctx, urlElicitation)
		req.Params.MultiRoundTripParams = relay.answer(target)
	}
	outer, err := client.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("muster: call %s: %w", tool, err)
	}
	if outer.NeedsInput() {
		relayed, err := urlelicitation.FromInputRequired(outer)
		if err != nil {
			return nil, fmt.Errorf("muster: call %s: %w", tool, err)
		}
		if relay != nil {
			relay.record(target, relayed)
		}
		return nil, fmt.Errorf("muster did not run %s: %s Open %s and then retry", tool, relayed.Message, relayed.URL)
	}
	inner, dispatched := unwrap(outer)
	if inner.IsError {
		if !dispatched {
			return nil, fmt.Errorf("muster did not run %s: %s", tool, textOf(inner))
		}
		return nil, fmt.Errorf("%s: %s", tool, textOf(inner))
	}
	raw := structuredOf(inner)
	if t := strings.TrimSpace(textOf(inner)); len(raw) == 0 && strings.HasPrefix(t, "{") && json.Valid([]byte(t)) {
		raw = json.RawMessage(t)
	}
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

// urlElicitation is what a call on a context from WithRelay declares to
// muster.
var urlElicitation = mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{URL: &struct{}{}}}

// Relay binds the first URL elicitation to its originating call and sends
// the caller's answer only to that call, once per handler invocation.
type Relay struct {
	mu          sync.Mutex
	retry       *urlelicitation.Retry
	answered    bool
	elicitation *Elicitation
}

// Elicitation is a URL interaction requested by muster.
type Elicitation struct {
	// Target names the call: the tool and a digest of its arguments.
	Target string
	// Relayed is muster's URL elicitation and requestState.
	Relayed *urlelicitation.Relayed
}

type relayKey struct{}

// WithRelay records URL elicitations and forwards a matching retry once.
func WithRelay(ctx context.Context, retry *urlelicitation.Retry) (context.Context, *Relay) {
	r := &Relay{retry: retry}
	return context.WithValue(ctx, relayKey{}, r), r
}

func relayFrom(ctx context.Context) *Relay {
	r, _ := ctx.Value(relayKey{}).(*Relay)
	return r
}

// Elicitation returns the first pending URL interaction, or nil.
func (r *Relay) Elicitation() *Elicitation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.elicitation
}

// answer is the caller's answer for the call target, once.
func (r *Relay) answer(target string) mcp.MultiRoundTripParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retry == nil || r.answered || r.retry.Target != target {
		return mcp.MultiRoundTripParams{}
	}
	r.answered = true
	return r.retry.Params
}

func (r *Relay) record(target string, relayed *urlelicitation.Relayed) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.elicitation == nil {
		r.elicitation = &Elicitation{Target: target, Relayed: relayed}
	}
}
