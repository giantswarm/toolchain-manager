package agentmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const prefix = "x_agent-manager_"

// fakeMuster serves muster's call_tool: it hands the target tool's call to
// answer and serialises the target's result into its own text, as muster
// does; a nil result is call_tool's own refusal.
type fakeMuster struct {
	mu      sync.Mutex
	bearers []string
	args    []map[string]any
	answer  func(tool string) *mcp.CallToolResult
}

func (f *fakeMuster) start(t *testing.T) *Client {
	t.Helper()
	s := mcpserver.NewMCPServer("muster", "test")
	s.AddTool(mcp.NewTool(metaCallTool), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		tool, _ := args["name"].(string)
		inner, _ := args["arguments"].(map[string]any)
		f.mu.Lock()
		f.args = append(f.args, inner)
		f.mu.Unlock()
		res := f.answer(tool)
		if res == nil {
			return mcp.NewToolResultError("tool " + tool + " not found"), nil
		}
		raw, err := json.Marshal(res)
		if err != nil {
			return nil, err
		}
		return mcp.NewToolResultText(string(raw)), nil
	})
	handler := mcpserver.NewStreamableHTTPServer(s)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.bearers = append(f.bearers, r.Header.Get("Authorization"))
		f.mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/mcp", prefix, "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUsersAsThePerson(t *testing.T) {
	f := &fakeMuster{answer: func(tool string) *mcp.CallToolResult {
		if tool != prefix+toolToolchainUsers {
			return nil
		}
		return mcp.NewToolResultStructured(map[string]any{
			"agents":   []map[string]any{{"namespace": "org-acme", "name": "coder", "toolchain": "go@v0.10.0"}},
			"sessions": []map[string]any{{"id": "s1", "namespace": "org-acme", "agent": "coder", "image": "r/go@sha256:aa", "state": "SUSPENDED"}},
		}, "1 agent, 1 session")
	}}
	c := f.start(t)

	u, err := c.Users(t.Context(), "dex", "go", "v0.10.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Agents) != 1 || u.Agents[0].Toolchain != "go@v0.10.0" || len(u.Sessions) != 1 || u.Sessions[0].State != "SUSPENDED" {
		t.Errorf("usage = %+v", u)
	}
	if !reflect.DeepEqual(f.args, []map[string]any{{"name": "go", "version": "v0.10.0"}}) {
		t.Errorf("args = %v", f.args)
	}
	for _, b := range f.bearers {
		if b != "Bearer dex" {
			t.Errorf("bearer = %q, want the person's on every request", b)
		}
	}

	if _, err := c.Users(t.Context(), "dex", "go", ""); err != nil || !reflect.DeepEqual(f.args[1], map[string]any{"name": "go"}) {
		t.Errorf("the whole toolchain: args %v, %v", f.args[1], err)
	}

	// agent-manager answers its JSON as text.
	f.answer = func(string) *mcp.CallToolResult {
		return mcp.NewToolResultText(`{"agents":[],"sessions":[{"id":"s2","namespace":"org-acme","agent":"coder","image":"r/go@sha256:bb"}]}`)
	}
	if u, err := c.Users(t.Context(), "dex", "go", ""); err != nil || len(u.Sessions) != 1 || u.Sessions[0].ID != "s2" {
		t.Errorf("a text answer = %+v, %v", u, err)
	}
}

func TestUsersRefused(t *testing.T) {
	for name, test := range map[string]struct {
		answer *mcp.CallToolResult
		bearer string
		err    string
	}{
		"not behind muster":   {answer: nil, bearer: "dex", err: "muster did not run x_agent-manager_toolchain_users"},
		"the tool refuses":    {answer: mcp.NewToolResultError("forbidden: no tenant"), bearer: "dex", err: "x_agent-manager_toolchain_users: forbidden: no tenant"},
		"no structured reply": {answer: mcp.NewToolResultText("ok"), bearer: "dex", err: "no structured result"},
		"no person":           {bearer: "", err: "no Dex ID token"},
	} {
		t.Run(name, func(t *testing.T) {
			c := (&fakeMuster{answer: func(string) *mcp.CallToolResult { return test.answer }}).start(t)
			if _, err := c.Users(t.Context(), test.bearer, "go", ""); err == nil || !strings.Contains(err.Error(), test.err) {
				t.Errorf("err = %v, want %q", err, test.err)
			}
		})
	}
}

func TestNewChecksItsConfiguration(t *testing.T) {
	for _, tc := range []struct{ url, prefix string }{
		{"ftp://muster/mcp", prefix},
		{"http://muster/mcp?x=1", prefix},
		{"http://muster/mcp", "agent-manager_"},
	} {
		if _, err := New(tc.url, tc.prefix, "test"); err == nil {
			t.Errorf("New(%q, %q) accepted", tc.url, tc.prefix)
		}
	}
}
