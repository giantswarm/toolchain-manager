package toolchains

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/giantswarm/toolchain-manager/api/v1alpha1"
	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/identity"
	"github.com/giantswarm/toolchain-manager/internal/sources"
)

const ns = "agent-platform"

var alice = &identity.Identity{Subject: "a", Email: "alice@example.com", EmailVerified: true}

// handWritten is a hand-written Dockerfile's tail, as giantswarm/agent-toolchains'
// go toolchain labels its image.
const handWritten = `FROM alpine:3.24 AS build
LABEL stage="build"
# The image is the mount directory.
FROM scratch
COPY --from=build /opt/kagent/tools/go/ /
LABEL io.giantswarm.toolchain.name="go" \
      io.giantswarm.toolchain.harnesses="kagent,claude,codex,omp" \
      io.giantswarm.toolchain.egress="https://proxy.golang.org,https://sum.golang.org" \
      io.giantswarm.toolchain.tool.go="go version && go env GOROOT" \
      io.giantswarm.toolchain.tool.gofmt="echo 'package p' | gofmt" \
      io.giantswarm.toolchain.tool.say="echo \"hi\" \$HOME"
`

// gitHubFor answers the token "grant": giantswarm/agent-toolchains publishes
// go (two versions, a draft and a pre-release beside them), acme/tools
// publishes helm and go, acme/pending publishes jq, acme/gone is not reached.
func gitHubFor(t *testing.T) *github.Client {
	t.Helper()
	written, err := os.ReadFile("../api/testdata/golden/toolchains/helm/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer grant" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/giantswarm/agent-toolchains/releases?per_page=100&page=1":
			_, _ = w.Write([]byte(`[
				{"tag_name":"go/v0.10.0","html_url":"https://github.com/giantswarm/agent-toolchains/releases/tag/go/v0.10.0","published_at":"2026-10-22T16:29:00Z"},
				{"tag_name":"go/v0.11.0","draft":true},
				{"tag_name":"go/v0.12.0-rc.1","prerelease":true},
				{"tag_name":"go/v0.9.0","published_at":"2026-10-20T10:00:00Z"},
				{"tag_name":"v1.0.0"}
			]`))
		case "/repos/acme/tools/releases?per_page=100&page=1":
			_, _ = w.Write([]byte(`[{"tag_name":"helm/v0.1.0","published_at":"2026-10-23T08:00:00Z"},{"tag_name":"go/v0.1.0"}]`))
		case "/repos/acme/pending/releases?per_page=100&page=1":
			_, _ = w.Write([]byte(`[{"tag_name":"jq/v0.1.0"}]`))
		case "/repos/giantswarm/agent-toolchains/contents/toolchains/go/Dockerfile?ref=go%2Fv0.9.0":
			if r.Header.Get("Accept") != "application/vnd.github.raw+json" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(handWritten))
		case "/repos/acme/tools/contents/toolchains/helm/Dockerfile?ref=helm%2Fv0.1.0":
			_, _ = w.Write(written)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &github.Client{API: srv.URL, HTTP: srv.Client()}
}

func newCatalog(t *testing.T, configured ...sources.Configured) *Catalog {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	src := func(name, repo string, phase v1alpha1.Phase) client.Object {
		return &v1alpha1.ToolchainSource{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       v1alpha1.ToolchainSourceSpec{Repository: repo, URL: "https://github.com/" + repo, Registry: "registry.acme.io/" + name},
			Status:     v1alpha1.ToolchainSourceStatus{Phase: phase},
		}
	}
	gh := gitHubFor(t)
	m := &sources.Manager{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.ToolchainSource{}).WithObjects(
			src("acme-tools", "acme/tools", v1alpha1.PhaseApproved),
			src("acme-pending", "acme/pending", v1alpha1.PhasePending),
		).Build(),
		Namespace:  ns,
		Configured: configured,
		GitHub:     gh,
		GrantApp:   "github",
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	return &Catalog{Sources: m, GitHub: gh}
}

var platform = sources.Configured{Repository: "giantswarm/agent-toolchains", Registry: "gsoci.azurecr.io/giantswarm/agent-toolchains"}

func as(grant string) context.Context {
	return identity.ContextWithGrant(identity.ContextWith(context.Background(), alice), grant)
}

func TestListShowsTheOfferedSourcesPublishedToolchains(t *testing.T) {
	c := newCatalog(t, platform, sources.Configured{Repository: "acme/gone", Registry: "registry.acme.io/gone"})
	res, err := c.List(as("grant"), "")
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ name, source, version, image string }
	var got []row
	for _, tc := range res.Toolchains {
		got = append(got, row{tc.Name, tc.Source, tc.Version, tc.Image})
	}
	want := []row{
		{"go", "acme-tools", "v0.1.0", "registry.acme.io/acme-tools/go:0.1.0"},
		{"go", "giantswarm-agent-toolchains", "v0.10.0", "gsoci.azurecr.io/giantswarm/agent-toolchains/go:0.10.0"},
		{"helm", "acme-tools", "v0.1.0", "registry.acme.io/acme-tools/helm:0.1.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("toolchains = %+v, want %+v (the pending source's jq not offered)", got, want)
	}
	platformGo := res.Toolchains[1]
	if !reflect.DeepEqual(platformGo.Versions, []string{"v0.10.0", "v0.9.0"}) {
		t.Errorf("versions = %v, want v0.10.0, v0.9.0: no draft, no pre-release", platformGo.Versions)
	}
	if platformGo.PublishedAt == nil || platformGo.ReleaseURL == "" {
		t.Errorf("go = %+v, want its release", platformGo)
	}
	if len(res.Unreachable) != 1 || res.Unreachable[0].Repository != "acme/gone" {
		t.Errorf("unreachable = %+v, want acme/gone", res.Unreachable)
	}

	one, err := c.List(as("grant"), "acme/tools")
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Toolchains) != 2 || one.Toolchains[0].Source != "acme-tools" {
		t.Errorf("acme/tools = %+v", one.Toolchains)
	}
	if _, err := c.List(as("grant"), "acme-pending"); err == nil || !strings.Contains(err.Error(), "no offered source") {
		t.Errorf("a pending source listed: %v", err)
	}
}

func TestListNeedsTheGrant(t *testing.T) {
	c := newCatalog(t, platform)
	for name, grant := range map[string]string{"no grant in muster": "", "grant refused": "revoked"} {
		t.Run(name, func(t *testing.T) {
			res, err := c.List(as(grant), "")
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != sources.StatusGrantRequired || !reflect.DeepEqual(res.Apps, []string{"github"}) || len(res.Toolchains) != 0 {
				t.Errorf("res = %+v, want grant_required naming github", res)
			}
		})
	}
	if _, err := c.List(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "no caller") {
		t.Errorf("listed without a caller: %v", err)
	}
}

func TestDescribeReadsTheVersionsLabels(t *testing.T) {
	c := newCatalog(t, platform)

	res, err := c.Describe(as("grant"), "go", "giantswarm/agent-toolchains", "0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	d := res.Toolchain
	if d.Version != "v0.9.0" || d.Image != "gsoci.azurecr.io/giantswarm/agent-toolchains/go:0.9.0" || d.Written {
		t.Errorf("go = %+v", d)
	}
	if d.Dockerfile != "https://github.com/giantswarm/agent-toolchains/blob/go/v0.9.0/toolchains/go/Dockerfile" {
		t.Errorf("dockerfile = %s", d.Dockerfile)
	}
	wantTools := []Tool{{"go", "go version && go env GOROOT"}, {"gofmt", "echo 'package p' | gofmt"}, {"say", `echo "hi" $HOME`}}
	if !reflect.DeepEqual(d.Tools, wantTools) {
		t.Errorf("tools = %+v, want %+v", d.Tools, wantTools)
	}
	if !reflect.DeepEqual(d.Harnesses, []string{"kagent", "claude", "codex", "omp"}) || len(d.Egress) != 2 {
		t.Errorf("declaration = %+v", d.Declaration)
	}

	helm, err := c.Describe(as("grant"), "helm", "", "")
	if err != nil {
		t.Fatal(err)
	}
	h := helm.Toolchain
	if !h.Written || h.Version != "v0.1.0" || h.Packages["helm"] != "3.19.0-r9" || len(h.Tools) != 1 || h.Tools[0].Name != "helm" {
		t.Errorf("helm = %+v", h)
	}

	for name, tc := range map[string]struct{ name, source, version, err string }{
		"two sources publish it": {name: "go", err: "name the source"},
		"not published":          {name: "jq", err: "no published toolchain jq"},
		"no such version":        {name: "helm", version: "v9.9.9", err: "no published version v9.9.9"},
		"unnamed":                {err: "name the toolchain"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.Describe(as("grant"), tc.name, tc.source, tc.version); err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("err = %v, want %q", err, tc.err)
			}
		})
	}

	none, err := c.Describe(as(""), "helm", "", "")
	if err != nil || none.Status != sources.StatusGrantRequired {
		t.Errorf("without the grant = %+v, %v", none, err)
	}
}
