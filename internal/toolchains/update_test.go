package toolchains

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

	"github.com/giantswarm/toolchain-manager/api/v1alpha1"
	"github.com/giantswarm/toolchain-manager/internal/binaries"
	"github.com/giantswarm/toolchain-manager/internal/gateway"
	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/packages"
	"github.com/giantswarm/toolchain-manager/internal/packages/alpine"
	"github.com/giantswarm/toolchain-manager/internal/toolchain"
	"github.com/giantswarm/toolchain-manager/internal/updates"
)

// newUpdater checks against the recorded Alpine indexes and a GitHub whose
// golangci-lint's latest release is latest.
func newUpdater(t *testing.T, latest string) *updates.Updater {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/golangci/golangci-lint/releases/latest" {
			_, _ = w.Write([]byte(`{"tag_name":"` + latest + `"}`))
			return
		}
		tag, ok := strings.CutPrefix(r.URL.Path, "/repos/golangci/golangci-lint/releases/tags/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		var assets []map[string]string
		for _, arch := range []string{"amd64", "arm64"} {
			name := "golangci-lint-" + strings.TrimPrefix(tag, "v") + "-linux-" + arch + ".tar.gz"
			assets = append(assets, map[string]string{"name": name, "digest": "sha256:" + strings.Repeat("c", 64),
				"browser_download_url": "https://github.com/golangci/golangci-lint/releases/download/" + tag + "/" + name})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": assets})
	}))
	t.Cleanup(srv.Close)
	reg, err := packages.NewRegistry(alpine.New(alpine.Config{Fetcher: alpine.DirFetcher("../packages/alpine/testdata/apkindex")}))
	if err != nil {
		t.Fatal(err)
	}
	return &updates.Updater{Packages: reg, Writer: toolchain.Writer{Sources: reg}, Binaries: binaries.Resolver{API: srv.URL, Client: srv.Client()},
		GitHub: &github.Client{API: srv.URL, HTTP: srv.Client()}}
}

// lintDockerfile is a written toolchain lint: helm at helm, golangci-lint at
// golangci.
func lintDockerfile(t *testing.T, u *updates.Updater, helm, golangci string) string {
	t.Helper()
	v := strings.TrimPrefix(golangci, "v")
	asset := func(arch string) binaries.Asset {
		return binaries.Asset{Arch: arch, SHA256: strings.Repeat("1", 64),
			URL: "https://github.com/golangci/golangci-lint/releases/download/" + golangci + "/golangci-lint-" + v + "-linux-" + arch + ".tar.gz"}
	}
	d, err := u.Writer.Write(toolchain.Spec{
		Name: "lint", Source: "alpine", Branch: "v3.24", Harnesses: []string{"kagent"},
		Packages: []packages.Pin{{Name: "helm", Version: helm}},
		Binaries: []binaries.Binary{{Name: "golangci-lint", Version: golangci, From: "golangci/golangci-lint", Assets: []binaries.Asset{asset("amd64"), asset("arm64")}}},
		Tools:    []packages.Tool{{Name: "helm", Version: "helm version"}, {Name: "golangci-lint", Path: "usr/local/bin/golangci-lint", Version: "golangci-lint --version"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(d)
}

type asks struct{ got []gateway.Review }

func (a *asks) Ask(_ context.Context, r gateway.Review) (gateway.Posted, error) {
	a.got = append(a.got, r)
	return gateway.Posted{ID: "rv1", Channel: r.Channel, TS: "1.0"}, nil
}

// updateCatalog is acme/tools publishing lint v0.1.0 from dockerfile, its
// team team-tools in C0123ABCDE.
func updateCatalog(t *testing.T, latest, helm, golangci string) (*Catalog, *fakeSource, *asks) {
	t.Helper()
	c, f := acmeCatalog(t)
	c.Updater = newUpdater(t, latest)
	a := &asks{}
	c.Reviews, c.ApproveTool = a, "x_toolchain-manager_approve_update"
	ctx := context.Background()
	var src v1alpha1.ToolchainSource
	if err := c.Sources.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: "acme-tools"}, &src); err != nil {
		t.Fatal(err)
	}
	src.Spec.Team, src.Spec.SlackChannel = "team-tools", "C0123ABCDE"
	if err := c.Sources.Client.Update(ctx, &src); err != nil {
		t.Fatal(err)
	}
	d := lintDockerfile(t, c.Updater, helm, golangci)
	f.files["lint/v0.1.0:toolchains/lint/Dockerfile"] = d
	f.files["main:toolchains/lint/Dockerfile"] = d
	f.list = `[{"tag_name":"lint/v0.1.0","html_url":"https://github.com/acme/tools/releases/tag/lint/v0.1.0"}]`
	return c, f, a
}

func called(f *fakeSource, prefix string) []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func TestUpdateOpensAMinorUpdateThatMergesOnTheGreenCheck(t *testing.T) {
	c, f, a := updateCatalog(t, "v2.15.0", "3.19.0-r1", "v2.14.0")

	res, err := c.Update(as("grant"), "", "lint", nil)
	if err != nil {
		t.Fatal(err)
	}
	u := res.Update
	if u == nil || u.Pull != 7 || u.Branch != "toolchain-update/lint" || u.Major || !u.AutoMerge || u.From != "v0.1.0" || len(u.Changes) != 2 || u.Note != "" {
		t.Fatalf("update = %+v (%s)", u, res.Message)
	}
	pr := f.pulls[0]
	if pr["title"] != "feat(lint): update helm 3.19.0-r1 → 3.19.0-r9, golangci-lint v2.14.0 → v2.15.0" ||
		!strings.Contains(pr["body"].(string), "| binary `golangci-lint` | v2.14.0 | v2.15.0 |") {
		t.Errorf("pull request = %+v", pr)
	}
	committed, err := toolchain.Read([]byte(f.files["toolchain-update/lint:toolchains/lint/Dockerfile"]))
	if err != nil || committed.Packages[0].Version != "3.19.0-r9" || committed.Binaries[0].Version != "v2.15.0" {
		t.Errorf("committed spec = %+v, %v", committed, err)
	}
	if got := called(f, "POST /graphql"); len(got) != 1 || !strings.Contains(got[0], `"id":"PR_7"`) {
		t.Errorf("auto-merge calls = %q", got)
	}
	if len(a.got) != 0 {
		t.Errorf("a minor update asked the team: %+v", a.got)
	}

	if _, err := c.Update(as("grant"), "", "lint", nil); err == nil || !strings.Contains(err.Error(), "open update, https://github.com/acme/tools/pull/7") {
		t.Errorf("a second update = %v", err)
	}
}

func TestUpdateOfAMajorAsksTheTeamAndApproveMergesIt(t *testing.T) {
	c, f, a := updateCatalog(t, "v2.15.0", "3.19.0-r9", "v1.64.8")

	res, err := c.Update(as("grant"), "acme/tools", "lint", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u := res.Update; u == nil || !u.Major || u.AutoMerge || u.Asked != "C0123ABCDE" || f.pulls[0]["title"] != "feat(lint)!: update golangci-lint v1.64.8 → v2.15.0" {
		t.Fatalf("update = %+v, title %v (%s)", u, f.pulls[0]["title"], res.Message)
	}
	if got := called(f, "POST /graphql"); len(got) != 0 {
		t.Errorf("a major update armed auto-merge: %q", got)
	}
	if len(a.got) != 1 {
		t.Fatalf("asks = %+v", a.got)
	}
	ask := a.got[0]
	if ask.Team != "team-tools" || ask.Channel != "C0123ABCDE" || ask.Actor != "alice@example.com" || ask.Approve.Tool != "x_toolchain-manager_approve_update" ||
		ask.Approve.Arguments["repository"] != "acme/tools" || ask.Approve.Arguments["pull"] != 7 ||
		len(ask.PullRequests) != 1 || ask.PullRequests[0] != "https://github.com/acme/tools/pull/7" || !strings.Contains(ask.Text, "binary `golangci-lint` v1.64.8 → v2.15.0 — major") {
		t.Errorf("ask = %+v", ask)
	}

	// The Approve click calls approve_update as the member.
	if _, err := c.ApproveUpdate(as("grant"), "acme/tools", 7); err == nil || !strings.Contains(err.Error(), "you opened") {
		t.Errorf("the author's approval = %v", err)
	}
	f.login = "bob-gh"
	f.push = false
	if _, err := c.ApproveUpdate(as("grant"), "acme/tools", 7); err == nil || !strings.Contains(err.Error(), "cannot push") {
		t.Errorf("an approval without push access = %v", err)
	}
	f.push = true
	got, err := c.ApproveUpdate(as("grant"), "https://github.com/acme/tools", 7)
	if err != nil || got.ApprovedBy != "bob-gh" || !got.AutoMerge || !strings.Contains(got.Message, "approved by bob-gh") {
		t.Fatalf("approval = %+v, %v", got, err)
	}
	if reviews := called(f, "POST /pulls/7/reviews"); len(reviews) != 1 || !strings.Contains(reviews[0], `"event":"APPROVE"`) {
		t.Errorf("reviews = %q", reviews)
	}
	if merges := called(f, "POST /graphql"); len(merges) != 1 {
		t.Errorf("auto-merge calls = %q", merges)
	}

	// Only an update's pull request is approved.
	f.pulls[0]["head"] = map[string]any{"ref": "toolchain/lint", "sha": "head"}
	if _, err := c.ApproveUpdate(as("grant"), "acme/tools", 7); err == nil || !strings.Contains(err.Error(), "no toolchain update") {
		t.Errorf("approving another pull request = %v", err)
	}
}

func TestUpdateRefusals(t *testing.T) {
	c, _, _ := updateCatalog(t, "v2.15.0", "3.19.0-r9", "v2.15.0")
	if res, err := c.Update(as("grant"), "", "lint", nil); err != nil || res.Update != nil || !strings.Contains(res.Message, "up to date") {
		t.Errorf("an up-to-date toolchain = %+v, %v", res, err)
	}

	// A major update without a team channel waits for a review on GitHub.
	c, _, a := updateCatalog(t, "v2.15.0", "3.19.0-r9", "v1.64.8")
	c.Reviews = nil
	res, err := c.Update(as("grant"), "", "lint", nil)
	if err != nil || res.Update.Asked != "" || !strings.Contains(res.Update.Note, "approving review on GitHub") || len(a.got) != 0 {
		t.Errorf("a major update without the gateway = %+v, %v", res.Update, err)
	}

	c, f, _ := updateCatalog(t, "v2.15.0", "3.19.0-r1", "v2.15.0")
	f.files["lint/v0.1.0:toolchains/lint/Dockerfile"] = strings.ReplaceAll(handWritten, `name="go"`, `name="lint"`)
	if _, err := c.Update(as("grant"), "", "lint", nil); err == nil || !strings.Contains(err.Error(), "hand-written") {
		t.Errorf("a hand-written toolchain = %v", err)
	}

	c, f, _ = updateCatalog(t, "v2.15.0", "3.19.0-r1", "v2.15.0")
	f.files["main:toolchains/lint/Dockerfile"] += "\n"
	if _, err := c.Update(as("grant"), "", "lint", nil); err == nil || !strings.Contains(err.Error(), "changed since lint v0.1.0") {
		t.Errorf("a Dockerfile changed since its release = %v", err)
	}

	c, f, _ = updateCatalog(t, "v2.15.0", "3.19.0-r1", "v2.15.0")
	f.push = false
	if _, err := c.Update(as("grant"), "", "lint", nil); err == nil || !strings.Contains(err.Error(), "cannot push") {
		t.Errorf("an update without push access = %v", err)
	}
}

func TestListAndDescribeCarryTheOutdatedMark(t *testing.T) {
	c, _, _ := updateCatalog(t, "v2.15.0", "3.19.0-r1", "v2.15.0")
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	notified := &noticeLog{}
	c.Updates = &updates.Watch{Updater: c.Updater, Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Namespace: ns,
		Name: "toolchain-manager-updates", Notifier: notified, Interval: time.Hour}
	ctx := as("grant")

	list, err := c.List(ctx, ListOptions{Name: "lint"})
	if err != nil || len(list.Toolchains) != 1 || list.Toolchains[0].Outdated != nil {
		t.Fatalf("list before a check = %+v, %v", list, err)
	}
	c.Updates.Round(context.Background())
	list, err = c.List(ctx, ListOptions{Name: "lint"})
	if err != nil {
		t.Fatal(err)
	}
	o := list.Toolchains[0].Outdated
	if o == nil || len(o.Outdated) != 1 || o.Outdated[0].Name != "helm" || !strings.Contains(o.Message, "update_toolchain") {
		t.Fatalf("mark = %+v", o)
	}
	if len(notified.channels) != 1 || notified.channels[0] != "team-tools C0123ABCDE" {
		t.Errorf("notices = %q", notified.channels)
	}
	d, err := c.Describe(ctx, "lint", "", "")
	if err != nil || d.Toolchain.Outdated == nil || d.Toolchain.Outdated.Outdated[0].To != "3.19.0-r9" {
		t.Errorf("describe = %+v, %v", d.Toolchain, err)
	}
}

type noticeLog struct{ channels []string }

func (n *noticeLog) Notice(_ context.Context, team, channel, _, _ string) error {
	n.channels = append(n.channels, team+" "+channel)
	return nil
}
