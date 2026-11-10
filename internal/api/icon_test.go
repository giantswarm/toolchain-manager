package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/giantswarm/mcp-toolkit/brand"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/toolchain-manager/internal/packages"
)

func TestInitializeDeclaresTheGiantSwarmIcon(t *testing.T) {
	sources, err := packages.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	srv := NewMCPServer(&Service{Sources: sources}, "test")
	initReq := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)

	raw, err := json.Marshal(srv.HandleMessage(t.Context(), initReq))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result mcp.InitializeResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if got, want := resp.Result.ServerInfo.Icons, []mcp.Icon{brand.GiantSwarmIcon()}; !reflect.DeepEqual(got, want) {
		t.Fatalf("serverInfo.icons = %+v, want %+v", got, want)
	}
}
