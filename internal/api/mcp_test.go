package api

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/toolchain-manager/internal/binaries"
	"github.com/giantswarm/toolchain-manager/internal/packages"
	"github.com/giantswarm/toolchain-manager/internal/packages/alpine"
	"github.com/giantswarm/toolchain-manager/internal/source"
	"github.com/giantswarm/toolchain-manager/internal/toolchain"
)

// The golden files under testdata/golden: each toolchains/<case>/Dockerfile
// is built and checked with the toolchain contract in CI
// (.github/workflows/toolchain-contract.yaml); init/ is init_source's output.
var update = flag.Bool("update", false, "rewrite the golden files")

// golangciLint is the golangci-lint release the fake GitHub serves, with the
// real assets' URLs and digests, so the golden Dockerfile builds.
const golangciLint = `{"tag_name":"v2.15.0","assets":[
{"name":"golangci-lint-2.15.0-linux-amd64.tar.gz","digest":"sha256:9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea","browser_download_url":"https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-amd64.tar.gz"},
{"name":"golangci-lint-2.15.0-linux-arm64.tar.gz","digest":"sha256:b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998","browser_download_url":"https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-arm64.tar.gz"}]}`

func newTestClient(t *testing.T, extra ...packages.Source) *client.Client {
	t.Helper()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/golangci/golangci-lint/releases/tags/v2.15.0" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(golangciLint))
	}))
	t.Cleanup(gh.Close)

	sources, err := packages.NewRegistry(append([]packages.Source{
		alpine.New(alpine.Config{Fetcher: alpine.DirFetcher("../packages/alpine/testdata/apkindex")}),
	}, extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Sources:       sources,
		Writer:        toolchain.Writer{Sources: sources},
		Binaries:      binaries.Resolver{API: gh.URL, Client: gh.Client()},
		DefaultSource: alpine.Name,
	}
	c, err := client.NewInProcessClient(NewMCPServer(svc, "test"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// call calls a tool and decodes its JSON answer into out; it returns the
// tool error's text, empty for a success.
func call(t *testing.T, c *client.Client, tool string, args map[string]any, out any) string {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if res.IsError {
		return text
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		t.Fatalf("%s: %v: %s", tool, err, text)
	}
	return ""
}

func golden(t *testing.T, path, got string) {
	t.Helper()
	path = filepath.Join("testdata", "golden", path)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // a golden file of this package
	if err != nil {
		t.Fatalf("%v (run go test ./internal/api -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden file (run go test ./internal/api -update and review the diff)", path)
	}
}

func TestSearchPackagesHelm(t *testing.T) {
	c := newTestClient(t)
	var res SearchResult
	if e := call(t, c, ToolSearchPackages, map[string]any{"query": "helm"}, &res); e != "" {
		t.Fatal(e)
	}
	if res.Packages[0].Name != "helm" || res.Packages[0].Versions["v3.24"] != "3.19.0-r9" || res.Packages[0].Versions["v3.22"] != "3.18.4-r5" {
		t.Errorf("helm %+v, want 3.19.0-r9 on v3.24 and 3.18.4-r5 on v3.22", res.Packages[0])
	}
	for _, branch := range []string{"v3.24", "v3.22"} {
		cl, ok := res.Closures[branch]
		if !ok {
			t.Fatalf("no closure on %s", branch)
		}
		if len(cl.Packages) != 2 || cl.Packages[0].Name != "helm" || cl.Packages[1].Name != "musl" {
			t.Errorf("helm's closure on %s: %+v", branch, cl.Packages)
		}
		if len(cl.Libraries) != 1 || cl.Libraries[0].Provider != "musl" {
			t.Errorf("helm's libraries on %s: %+v", branch, cl.Libraries)
		}
	}
}

func TestAddPackagesAndBinaryWriteTheGoldenDockerfiles(t *testing.T) {
	c := newTestClient(t)

	var helm ToolchainResult
	if e := call(t, c, ToolAddPackages, map[string]any{"name": "helm", "packages": []string{"helm"}}, &helm); e != "" {
		t.Fatal(e)
	}
	golden(t, "toolchains/helm/Dockerfile", helm.Dockerfile)
	if helm.Spec.Packages[0] != (packages.Pin{Name: "helm", Version: "3.19.0-r9"}) || helm.Spec.Branch != "v3.24" {
		t.Errorf("helm pinned %+v on %s, want 3.19.0-r9 on v3.24", helm.Spec.Packages, helm.Spec.Branch)
	}

	var python ToolchainResult
	if e := call(t, c, ToolAddPackages, map[string]any{
		"name": "python", "branch": "v3.22", "packages": []string{"python3", "py3-pytest=8.3.5-r0"},
	}, &python); e != "" {
		t.Fatal(e)
	}
	golden(t, "toolchains/python/Dockerfile", python.Dockerfile)

	var lint ToolchainResult
	if e := call(t, c, ToolAddBinary, map[string]any{
		"name": "golangci-lint", "binary": "golangci-lint", "version": "v2.15.0",
		"repository": "golangci/golangci-lint", "asset": "golangci-lint-{version}-linux-{arch}.tar.gz",
	}, &lint); e != "" {
		t.Fatal(e)
	}
	golden(t, "toolchains/golangci-lint/Dockerfile", lint.Dockerfile)

	// go: packages, then a binary added to the Dockerfile add_packages wrote.
	var goTC ToolchainResult
	if e := call(t, c, ToolAddPackages, map[string]any{"name": "go", "packages": []string{"go"}}, &goTC); e != "" {
		t.Fatal(e)
	}
	if e := call(t, c, ToolAddBinary, map[string]any{
		"dockerfile": goTC.Dockerfile, "binary": "golangci-lint", "version": "v2.15.0",
		"repository": "golangci/golangci-lint", "asset": "golangci-lint-{version}-linux-{arch}.tar.gz",
	}, &goTC); e != "" {
		t.Fatal(e)
	}
	golden(t, "toolchains/go/Dockerfile", goTC.Dockerfile)
	var names []string
	for _, tool := range goTC.Spec.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "go,golangci-lint" {
		t.Errorf("go's tools %v, want go and golangci-lint", names)
	}
	for _, want := range []string{
		`sha256: `, `amd64) set -- 'https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-amd64.tar.gz' 9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea`,
		`go=1.26.9-r0`, `GOROOT=/opt/kagent/tools/go/usr/lib/go`, `rm -f /opt/kagent/tools/go/lib/apk/db/lock`,
		`io.giantswarm.toolchain.tool.golangci-lint="golangci-lint --version"`,
		"FROM scratch\nCOPY --from=build /opt/kagent/tools/go/ /",
	} {
		if !strings.Contains(goTC.Dockerfile, want) {
			t.Errorf("go's Dockerfile lacks %q", want)
		}
	}
}

func TestTheWrittenDockerfileRemovesApksLock(t *testing.T) {
	c := newTestClient(t)
	var res ToolchainResult
	if e := call(t, c, ToolAddPackages, map[string]any{"name": "jq", "branch": "v3.22", "packages": []string{"jq"}}, &res); e != "" {
		t.Fatal(e)
	}
	install := res.Dockerfile[strings.Index(res.Dockerfile, "apk add"):]
	if !strings.Contains(install[:strings.Index(install, "\n\n")], "rm -f /opt/kagent/tools/jq/lib/apk/db/lock") {
		t.Errorf("the install step does not remove apk's lock:\n%s", install)
	}
}

func TestAddPackagesRefuses(t *testing.T) {
	c := newTestClient(t)
	var res ToolchainResult
	for name, args := range map[string]map[string]any{
		"another version":   {"name": "helm", "packages": []string{"helm=3.0.0-r0"}},
		"an unknown branch": {"name": "helm", "branch": "v2.0", "packages": []string{"helm"}},
		"no toolchain":      {"packages": []string{"helm"}},
		"a hand-written Dockerfile": {
			"dockerfile": "FROM scratch\nCOPY . /\n", "packages": []string{"helm"},
		},
	} {
		if e := call(t, c, ToolAddPackages, args, &res); e == "" {
			t.Errorf("%s: accepted", name)
		}
	}

	// A hand edit of a written Dockerfile makes it hand-written.
	if e := call(t, c, ToolAddPackages, map[string]any{"name": "helm", "packages": []string{"helm"}}, &res); e != "" {
		t.Fatal(e)
	}
	edited := strings.Replace(res.Dockerfile, "FROM scratch", "FROM scratch\nUSER 0", 1)
	if e := call(t, c, ToolAddPackages, map[string]any{"dockerfile": edited, "packages": []string{"jq"}}, &res); !strings.Contains(e, "hand-written") {
		t.Errorf("an edited Dockerfile: %q, want hand-written", e)
	}
}

// A Dockerfile that lost or gained trailing newlines, or took CRLF line
// endings, on its way back is still the manager's.
func TestAddPackagesTakesBackAWhitespaceChangedDockerfile(t *testing.T) {
	c := newTestClient(t)
	var res ToolchainResult
	if e := call(t, c, ToolAddPackages, map[string]any{"name": "helm", "packages": []string{"helm"}}, &res); e != "" {
		t.Fatal(e)
	}
	for name, dockerfile := range map[string]string{
		"without its last newline": strings.TrimRight(res.Dockerfile, "\n"),
		"with another newline":     res.Dockerfile + "\n",
		"with CRLF line endings":   strings.ReplaceAll(res.Dockerfile, "\n", "\r\n"),
	} {
		var again ToolchainResult
		if e := call(t, c, ToolAddPackages, map[string]any{"dockerfile": dockerfile, "packages": []string{"jq"}}, &again); e != "" {
			t.Errorf("%s: %s", name, e)
		}
	}
}

// fakeSource is a second package source, plugged in behind the interface.
type fakeSource struct{}

func (fakeSource) Name() string        { return "fake" }
func (fakeSource) Branches() []string  { return []string{"stable"} }
func (fakeSource) Harnesses() []string { return []string{"claude"} }
func (fakeSource) Search(context.Context, string) ([]packages.Package, error) {
	return []packages.Package{{Name: "tool", Versions: map[string]string{"stable": "1.0"}}}, nil
}
func (fakeSource) Closure(_ context.Context, branch string, names []string) (packages.Closure, error) {
	c := packages.Closure{Branch: branch}
	for _, n := range names {
		c.Packages = append(c.Packages, packages.ClosurePackage{Name: n, Version: "1.0", Requested: true})
	}
	return c, nil
}
func (fakeSource) Tools(_ context.Context, _, name string) ([]packages.Tool, error) {
	return []packages.Tool{{Name: name, Version: name + " -v"}}, nil
}
func (fakeSource) Install(_, prefix string, pins []packages.Pin) (packages.Install, error) {
	return packages.Install{Base: "registry.example.com/fake@sha256:" + strings.Repeat("0", 64), Run: "fake-install --root " + prefix + " " + pins[0].String()}, nil
}

func TestASecondPackageSourcePlugsIn(t *testing.T) {
	c := newTestClient(t, fakeSource{})
	var found SearchResult
	if e := call(t, c, ToolSearchPackages, map[string]any{"query": "tool", "source": "fake"}, &found); e != "" {
		t.Fatal(e)
	}
	if found.Source != "fake" || found.Closures["stable"].Packages[0].Name != "tool" {
		t.Errorf("search of the fake source: %+v", found)
	}
	var res ToolchainResult
	if e := call(t, c, ToolAddPackages, map[string]any{"name": "tool", "source": "fake", "packages": []string{"tool"}}, &res); e != "" {
		t.Fatal(e)
	}
	for _, want := range []string{"FROM registry.example.com/fake@sha256:", "fake-install --root /opt/kagent/tools/tool tool=1.0", `io.giantswarm.toolchain.harnesses="claude"`} {
		if !strings.Contains(res.Dockerfile, want) {
			t.Errorf("the fake source's Dockerfile lacks %q:\n%s", want, res.Dockerfile)
		}
	}
}

func TestInitSourceWritesTheGoldenTemplate(t *testing.T) {
	c := newTestClient(t)
	var res struct {
		Files []source.File `json:"files"`
	}
	if e := call(t, c, ToolInitSource, map[string]any{
		"repository": "example/toolchains", "registry": "registry.example.com/example/toolchains", "renovate": true,
	}, &res); e != "" {
		t.Fatal(e)
	}
	var paths []string
	for _, f := range res.Files {
		paths = append(paths, f.Path)
		golden(t, filepath.Join("init", f.Path), f.Content)
	}
	want := ".github/workflows/toolchain-contract.yaml,.github/workflows/toolchain-release.yaml,README.md,renovate.json5,toolchains/jq/Dockerfile"
	if strings.Join(paths, ",") != want {
		t.Errorf("init_source wrote %v, want %s", paths, want)
	}

	if e := call(t, c, ToolInitSource, map[string]any{"repository": "example/toolchains", "registry": "registry.example.com/x"}, &res); e != "" {
		t.Fatal(e)
	}
	for _, f := range res.Files {
		if f.Path == "renovate.json5" {
			t.Error("renovate.json5 without renovate")
		}
	}
	if e := call(t, c, ToolInitSource, map[string]any{"repository": "nope", "registry": "registry.example.com/x"}, &res); e == "" {
		t.Error("a repository without an owner accepted")
	}
}
