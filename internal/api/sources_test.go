package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/giantswarm/toolchain-manager/api/v1alpha1"
	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/identity"
	"github.com/giantswarm/toolchain-manager/internal/packages"
	"github.com/giantswarm/toolchain-manager/internal/packages/alpine"
	"github.com/giantswarm/toolchain-manager/internal/sources"
	"github.com/giantswarm/toolchain-manager/internal/toolchains"
)

func TestSourceToolsAnswerAsTheCaller(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer grant" && r.URL.Path == "/repos/acme/tools" {
			_, _ = w.Write([]byte(`{"full_name":"acme/tools","html_url":"https://github.com/acme/tools","permissions":{"push":true}}`))
			return
		}
		if r.Header.Get("Authorization") == "Bearer grant" && r.URL.Path == "/repos/acme/tools/releases" {
			_, _ = w.Write([]byte(`[{"tag_name":"jq/v0.2.0"},{"tag_name":"jq/v0.1.0"}]`))
			return
		}
		http.NotFound(w, r)
	}))
	defer gh.Close()
	var err error
	scheme := runtime.NewScheme()
	if err = v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	svc := &Service{ToolchainSources: &sources.Manager{
		Client:    fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.ToolchainSource{}).Build(),
		Namespace: "agent-platform",
		Owners:    sources.Owners{Groups: []string{"platform-admins"}},
		GitHub:    &github.Client{API: gh.URL, HTTP: gh.Client()},
		GrantApp:  "github",
	}}
	svc.Toolchains = &toolchains.Catalog{Sources: svc.ToolchainSources, GitHub: svc.ToolchainSources.GitHub}
	if svc.Sources, err = packages.NewRegistry(alpine.New(alpine.Config{Fetcher: alpine.DirFetcher("../packages/alpine/testdata/apkindex")})); err != nil {
		t.Fatal(err)
	}
	svc.DefaultSource = alpine.Name
	c, err := client.NewInProcessClient(NewMCPServer(svc, "test"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	if _, err := c.Initialize(context.Background(), init); err != nil {
		t.Fatal(err)
	}

	tools, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]mcp.Tool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = tool
	}
	for _, want := range []string{ToolListSources, ToolAddSource, ToolApproveSource, ToolRemoveSource, ToolListToolchains, ToolDescribeToolchain, ToolCreateToolchain, ToolBuildStatus} {
		if _, ok := names[want]; !ok {
			t.Errorf("%s not served", want)
		}
	}
	if d := names[ToolRemoveSource].Annotations.DestructiveHint; d == nil || !*d {
		t.Errorf("remove_source is not marked destructive")
	}

	dev := identity.ContextWith(context.Background(), &identity.Identity{Subject: "d", Email: "dev@lab.local", EmailVerified: true})
	args := map[string]any{"repository": "acme/tools", "registry": "registry.acme.io/toolchains", "slackChannel": "#team-tools"}

	var res sources.AddResult
	callAs(t, c, dev, ToolAddSource, args, &res)
	if res.Status != sources.StatusGrantRequired || len(res.Apps) != 1 || res.Apps[0] != "github" {
		t.Errorf("without the grant: %+v", res)
	}

	callAs(t, c, identity.ContextWithGrant(dev, "grant"), ToolAddSource, args, &res)
	if res.Source == nil || res.Source.Phase != v1alpha1.PhasePending {
		t.Fatalf("with the grant: %+v", res)
	}

	admin := identity.ContextWith(context.Background(), &identity.Identity{Subject: "a", Email: "admin@lab.local", EmailVerified: true, Groups: []string{"platform-admins"}})
	var approved sources.Source
	callAs(t, c, admin, ToolApproveSource, map[string]any{"source": "acme/tools"}, &approved)
	if !approved.Offered || approved.ApprovedBy != "admin@lab.local" {
		t.Errorf("approved: %+v", approved)
	}

	var list SourcesResult
	callAs(t, c, dev, ToolListSources, nil, &list)
	if len(list.Sources) != 1 || !list.Sources[0].Offered {
		t.Errorf("list: %+v", list)
	}

	var offered toolchains.ListResult
	callAs(t, c, identity.ContextWithGrant(dev, "grant"), ToolListToolchains, nil, &offered)
	if len(offered.Toolchains) != 1 || offered.Toolchains[0].Name != "jq" || offered.Toolchains[0].Image != "registry.acme.io/toolchains/jq:0.2.0" {
		t.Errorf("list_toolchains: %+v", offered)
	}

	var removed sources.Source
	callAs(t, c, dev, ToolRemoveSource, map[string]any{"source": "acme-tools"}, &removed)
	callAs(t, c, dev, ToolListSources, nil, &list)
	if len(list.Sources) != 0 {
		t.Errorf("after remove_source: %+v", list)
	}
}

// callAs calls a tool with ctx's caller and decodes its structured answer.
func callAs(t *testing.T, c *client.Client, ctx context.Context, tool string, args map[string]any, out any) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%s: %s", tool, res.Content[0].(mcp.TextContent).Text)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
}
