package updates

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/giantswarm/toolchain-manager/internal/binaries"
	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/packages"
	"github.com/giantswarm/toolchain-manager/internal/packages/alpine"
	"github.com/giantswarm/toolchain-manager/internal/toolchain"
)

// releases answers golangci-lint's latest release (latest) and every
// release's assets, as GitHub does anonymously.
func releases(t *testing.T, latest string) (*github.Client, binaries.Resolver) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tag := ""
		switch {
		case r.URL.Path == "/repos/golangci/golangci-lint/releases/latest":
			_, _ = w.Write([]byte(`{"tag_name":"` + latest + `"}`))
			return
		case strings.HasPrefix(r.URL.Path, "/repos/golangci/golangci-lint/releases/tags/"):
			tag = strings.TrimPrefix(r.URL.Path, "/repos/golangci/golangci-lint/releases/tags/")
		default:
			http.NotFound(w, r)
			return
		}
		v := strings.TrimPrefix(tag, "v")
		var assets []map[string]string
		for i, arch := range []string{"amd64", "arm64"} {
			name := "golangci-lint-" + v + "-linux-" + arch + ".tar.gz"
			assets = append(assets, map[string]string{
				"name": name, "browser_download_url": "https://github.com/golangci/golangci-lint/releases/download/" + tag + "/" + name,
				"digest": "sha256:" + strings.Repeat(string(rune('a'+i)), 64),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": assets})
	}))
	t.Cleanup(srv.Close)
	return &github.Client{API: srv.URL, HTTP: srv.Client()}, binaries.Resolver{API: srv.URL, Client: srv.Client()}
}

func newUpdater(t *testing.T, latest string) *Updater {
	t.Helper()
	reg, err := packages.NewRegistry(alpine.New(alpine.Config{Fetcher: alpine.DirFetcher("../packages/alpine/testdata/apkindex")}))
	if err != nil {
		t.Fatal(err)
	}
	gh, resolver := releases(t, latest)
	return &Updater{Packages: reg, Writer: toolchain.Writer{Sources: reg}, Binaries: resolver, GitHub: gh}
}

// lint is a written toolchain pinning helm and jq of v3.24 and golangci-lint
// at a release.
func lint(t *testing.T, u *Updater, helm, golangci string) toolchain.Spec {
	t.Helper()
	v := strings.TrimPrefix(golangci, "v")
	spec := toolchain.Spec{
		Name: "lint", Source: "alpine", Branch: "v3.24", Harnesses: []string{"kagent", "claude"},
		Packages: []packages.Pin{{Name: "helm", Version: helm}, {Name: "jq", Version: "1.8.2-r0"}},
		Binaries: []binaries.Binary{{Name: "golangci-lint", Version: golangci, From: "golangci/golangci-lint", Assets: []binaries.Asset{
			{Arch: "amd64", URL: "https://github.com/golangci/golangci-lint/releases/download/" + golangci + "/golangci-lint-" + v + "-linux-amd64.tar.gz", SHA256: strings.Repeat("1", 64)},
			{Arch: "arm64", URL: "https://github.com/golangci/golangci-lint/releases/download/" + golangci + "/golangci-lint-" + v + "-linux-arm64.tar.gz", SHA256: strings.Repeat("2", 64)},
		}}},
		Tools: []packages.Tool{{Name: "helm", Version: "helm version"}, {Name: "jq", Version: "jq --version"}, {Name: "golangci-lint", Path: "usr/local/bin/golangci-lint", Version: "golangci-lint --version"}},
	}
	dockerfile, err := u.Writer.Write(spec)
	if err != nil {
		t.Fatal(err)
	}
	if spec, err = toolchain.Read(dockerfile); err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestCheckNamesTheOutdatedPins(t *testing.T) {
	u := newUpdater(t, "v2.15.0")
	st, err := u.Check(context.Background(), lint(t, u, "3.19.0-r1", "v1.64.8"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{
		{Kind: KindPackage, Name: "helm", From: "3.19.0-r1", To: "3.19.0-r9"},
		{Kind: KindBinary, Name: "golangci-lint", From: "v1.64.8", To: "v2.15.0", Major: true},
	}
	if len(st.Outdated) != len(want) || st.Outdated[0] != want[0] || st.Outdated[1] != want[1] || len(st.Unchecked) != 0 {
		t.Fatalf("status = %+v, want %+v", st, want)
	}

	current, err := u.Check(context.Background(), lint(t, u, "3.19.0-r9", "v2.15.0"))
	if err != nil || len(current.Outdated) != 0 {
		t.Errorf("a current toolchain = %+v, %v", current, err)
	}

	gone := lint(t, u, "3.19.0-r9", "v2.15.0")
	gone.Packages = append(gone.Packages, packages.Pin{Name: "no-such-package", Version: "1"})
	gone.Binaries[0].From = "https://example.com/golangci-lint"
	st, err = u.Check(context.Background(), gone)
	if err != nil || len(st.Unchecked) != 2 || !strings.Contains(st.Unchecked[0], "no-such-package") || !strings.Contains(st.Unchecked[1], "URL download") {
		t.Errorf("unchecked = %+v, %v", st, err)
	}
}

func TestPlanRepinsOnlyTheVersions(t *testing.T) {
	u := newUpdater(t, "v2.15.0")
	old := lint(t, u, "3.19.0-r1", "v2.14.0")
	old.Base = "docker.io/library/alpine:3.24.0@sha256:" + strings.Repeat("0", 64)

	plan, err := u.Plan(context.Background(), old, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Major() || len(plan.Changes) != 2 {
		t.Fatalf("changes = %+v", plan.Changes)
	}
	s := plan.Spec
	if s.Packages[0].Version != "3.19.0-r9" || s.Packages[1] != old.Packages[1] {
		t.Errorf("packages = %+v", s.Packages)
	}
	b := s.Binaries[0]
	if b.Version != "v2.15.0" || b.Assets[0].URL != "https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-amd64.tar.gz" ||
		b.Assets[0].SHA256 != strings.Repeat("a", 64) || b.Assets[1].Arch != "arm64" {
		t.Errorf("binary = %+v", b)
	}
	if s.Base == old.Base || !strings.HasPrefix(s.Base, "docker.io/library/alpine:3.24.3@") {
		t.Errorf("base = %s, want the source's current one", s.Base)
	}
	if len(s.Tools) != len(old.Tools) || s.Name != old.Name || strings.Join(s.Harnesses, ",") != strings.Join(old.Harnesses, ",") {
		t.Errorf("spec beyond the pins changed: %+v", s)
	}
	if !strings.Contains(string(plan.Dockerfile), `io.giantswarm.toolchain.binary.golangci-lint="v2.15.0"`) {
		t.Error("the Dockerfile does not label the new binary version")
	}

	only, err := u.Plan(context.Background(), old, []string{"helm"})
	if err != nil || len(only.Changes) != 1 || only.Spec.Binaries[0].Version != "v2.14.0" {
		t.Errorf("only helm = %+v, %v", only.Changes, err)
	}
	if _, err := u.Plan(context.Background(), old, []string{"jq"}); err == nil || !strings.Contains(err.Error(), "outdated: helm, golangci-lint") {
		t.Errorf("a current pin named = %v", err)
	}
	if _, err := u.Plan(context.Background(), lint(t, u, "3.19.0-r9", "v2.15.0"), nil); err != ErrUpToDate {
		t.Errorf("an up-to-date toolchain = %v", err)
	}
}

func TestMajorAndTemplates(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		major    bool
	}{
		{"v1.64.8", "v2.0.0", true}, {"v2.5.0", "v2.14.0", false}, {"1.24.3-r0", "1.25.0-r1", false},
		{"jq-1.7", "jq-1.8", false}, {"3.18.4-r5", "4.0.0-r0", true}, {"latest", "v1", true}, {"v09", "v9.1", false},
	} {
		if got := major(tc.from, tc.to); got != tc.major {
			t.Errorf("major(%s, %s) = %t", tc.from, tc.to, got)
		}
	}
	if got := templated("golangci-lint-2.5.0-linux-amd64.tar.gz", "v2.5.0", "amd64"); got != "golangci-lint-{version}-linux-{arch}.tar.gz" {
		t.Errorf("templated = %s", got)
	}
	if got := templated("tool_v1.2_Linux_x86_64.tgz", "v1.2", "amd64"); got != "tool_{tag}_Linux_{machine}.tgz" {
		t.Errorf("templated = %s", got)
	}
	if got := templatedPath("tool-1.2/tool", "v1.2", "v1.3"); got != "tool-1.3/tool" {
		t.Errorf("templatedPath = %s", got)
	}
}

type notices struct{ sent []string }

func (n *notices) Notice(_ context.Context, team, channel, text, link string) error {
	n.sent = append(n.sent, team+" "+channel+" "+link+"\n"+text)
	return nil
}

func newWatch(t *testing.T, u *Updater, objs ...client.Object) (*Watch, *notices, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	n := &notices{}
	now := time.Date(2026, 10, 23, 4, 0, 0, 0, time.UTC)
	return &Watch{Updater: u, Client: c, Namespace: "agent-platform", Name: "toolchain-manager-updates", Notifier: n, Interval: time.Hour, Now: func() time.Time { return now }}, n, c
}

func TestWatchMarksOutdatedAndTellsTheTeamOnce(t *testing.T) {
	u := newUpdater(t, "v2.15.0")
	w, n, c := newWatch(t, u)
	ctx := context.Background()
	spec := lint(t, u, "3.19.0-r1", "v2.15.0")
	watched := Watched{Source: "acme-tools", Repository: "acme/tools", Toolchain: "lint", Version: "v0.1.0", ReleaseURL: "https://github.com/acme/tools/releases/tag/lint/v0.1.0",
		Team: "team-tools", SlackChannel: "C0123ABCDE", Spec: &spec}

	w.Observe(ctx, watched)
	if m := w.Mark(ctx, "acme/tools", "lint", "v0.1.0"); m != nil {
		t.Errorf("marked before a check: %+v", m)
	}
	w.Round(ctx)
	m := w.Mark(ctx, "acme/tools", "lint", "v0.1.0")
	if m == nil || len(m.Outdated) != 1 || m.Outdated[0].Name != "helm" || !strings.Contains(m.Message, "lint v0.1.0 is outdated: helm 3.19.0-r1 → 3.19.0-r9") || m.CheckedAt == nil {
		t.Fatalf("mark = %+v", m)
	}
	if w.Mark(ctx, "acme/tools", "lint", "v0.0.9") != nil {
		t.Error("another version is marked")
	}
	if len(n.sent) != 1 || !strings.HasPrefix(n.sent[0], "team-tools C0123ABCDE https://github.com/acme/tools/releases/tag/lint/v0.1.0\n") ||
		!strings.Contains(n.sent[0], "package `helm` 3.19.0-r1 → 3.19.0-r9") {
		t.Fatalf("notices = %q", n.sent)
	}

	w.Round(ctx)
	w.Observe(ctx, watched)
	if len(n.sent) != 1 {
		t.Errorf("told the team again: %q", n.sent)
	}

	// The state outlives the process: a new watch reads the ConfigMap.
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "agent-platform", Name: "toolchain-manager-updates"}, &cm); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cm.Data["acme.tools.lint"], `"notified":"package:helm=3.19.0-r9"`) {
		t.Errorf("state = %v", cm.Data)
	}
	again, n2, _ := newWatch(t, u, &cm)
	again.Round(ctx)
	if again.Mark(ctx, "acme/tools", "lint", "v0.1.0") == nil || len(n2.sent) != 0 {
		t.Errorf("after a restart: mark %v, notices %q", again.Mark(ctx, "acme/tools", "lint", "v0.1.0"), n2.sent)
	}

	// The next release is current: its mark clears and nothing is sent.
	current := lint(t, u, "3.19.0-r9", "v2.15.0")
	watched.Version, watched.Spec = "v0.1.1", &current
	w.Observe(ctx, watched)
	w.Round(ctx)
	if m := w.Mark(ctx, "acme/tools", "lint", "v0.1.1"); m != nil || len(n.sent) != 1 {
		t.Errorf("a current version: mark %+v, notices %d", m, len(n.sent))
	}

	// A hand-written toolchain is watched without a check.
	w.Observe(ctx, Watched{Source: "acme-tools", Repository: "acme/tools", Toolchain: "go", Version: "v1.0.0"})
	w.Round(ctx)
	if w.Mark(ctx, "acme/tools", "go", "v1.0.0") != nil {
		t.Error("a hand-written toolchain is marked")
	}
}

func TestWatchWithoutAChannelMarksOnly(t *testing.T) {
	u := newUpdater(t, "v2.15.0")
	w, n, _ := newWatch(t, u)
	ctx := context.Background()
	spec := lint(t, u, "3.19.0-r1", "v2.15.0")
	w.Observe(ctx, Watched{Source: "acme-tools", Repository: "acme/tools", Toolchain: "lint", Version: "v0.1.0", Spec: &spec})
	w.Round(ctx)
	if w.Mark(ctx, "acme/tools", "lint", "v0.1.0") == nil || len(n.sent) != 0 {
		t.Errorf("mark %v, notices %q", w.Mark(ctx, "acme/tools", "lint", "v0.1.0"), n.sent)
	}
}
