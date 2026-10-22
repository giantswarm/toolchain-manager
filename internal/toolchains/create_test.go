package toolchains

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/registry"
	"github.com/giantswarm/toolchain-manager/internal/sources"
)

// fakeSource is acme/tools as GitHub answers the token "grant": main at
// "base" with toolchain helm, the branches, files and pull requests the
// calls make, and the CI results, tags and releases a test sets.
type fakeSource struct {
	mu       sync.Mutex
	push     bool
	branches map[string]string
	files    map[string]string // branch:path → content
	pulls    []map[string]any
	jobs     map[string]string // sha → check-runs JSON
	statuses map[string]string // sha → statuses JSON
	tags     string            // matching-refs JSON
	releases map[string]string // tag → release JSON
	calls    []string
}

func newFakeSource(t *testing.T) (*fakeSource, *github.Client) {
	t.Helper()
	f := &fakeSource{
		push:     true,
		branches: map[string]string{"main": "base"},
		files:    map[string]string{"main:toolchains/helm/Dockerfile": "FROM scratch"},
		jobs:     map[string]string{},
		statuses: map[string]string{},
		tags:     "[]",
		releases: map[string]string{},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, &github.Client{API: srv.URL, HTTP: srv.Client()}
}

func (f *fakeSource) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer grant" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/repos/acme/tools")
	f.calls = append(f.calls, r.Method+" "+path)
	var in map[string]any
	_ = json.NewDecoder(r.Body).Decode(&in)
	write := func(v string) { _, _ = w.Write([]byte(v)) }
	switch {
	case r.Method == http.MethodGet && path == "":
		write(fmt.Sprintf(`{"full_name":"acme/tools","default_branch":"main","permissions":{"push":%t}}`, f.push))
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/contents/"):
		content, ok := f.files[r.URL.Query().Get("ref")+":"+strings.TrimPrefix(path, "/contents/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		write(content)
	case r.Method == http.MethodPut && strings.HasPrefix(path, "/contents/"):
		b, _ := base64.StdEncoding.DecodeString(in["content"].(string))
		f.files[in["branch"].(string)+":"+strings.TrimPrefix(path, "/contents/")] = string(b)
		w.WriteHeader(http.StatusCreated)
		write(`{}`)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/git/ref/heads/"):
		sha, ok := f.branches[strings.TrimPrefix(path, "/git/ref/heads/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		write(fmt.Sprintf(`{"object":{"sha":%q}}`, sha))
	case r.Method == http.MethodPost && path == "/git/refs":
		f.branches[strings.TrimPrefix(in["ref"].(string), "refs/heads/")] = in["sha"].(string)
		w.WriteHeader(http.StatusCreated)
		write(`{}`)
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/git/refs/heads/"):
		f.branches[strings.TrimPrefix(path, "/git/refs/heads/")] = in["sha"].(string)
		write(`{}`)
	case r.Method == http.MethodPost && path == "/pulls":
		pr := map[string]any{"number": 7 + len(f.pulls), "html_url": fmt.Sprintf("https://github.com/acme/tools/pull/%d", 7+len(f.pulls)),
			"title": in["title"], "body": in["body"], "state": "open", "head": map[string]any{"ref": in["head"], "sha": "head"}, "base": in["base"]}
		f.pulls = append([]map[string]any{pr}, f.pulls...)
		w.WriteHeader(http.StatusCreated)
		b, _ := json.Marshal(pr)
		_, _ = w.Write(b)
	case r.Method == http.MethodGet && path == "/pulls":
		var out []map[string]any
		for _, pr := range f.pulls {
			if "acme:"+pr["head"].(map[string]any)["ref"].(string) == r.URL.Query().Get("head") {
				out = append(out, pr)
			}
		}
		b, _ := json.Marshal(append([]map[string]any{}, out...))
		_, _ = w.Write(b)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/pulls/"):
		for _, pr := range f.pulls {
			if fmt.Sprint(pr["number"]) == strings.TrimPrefix(path, "/pulls/") {
				b, _ := json.Marshal(pr)
				_, _ = w.Write(b)
				return
			}
		}
		http.NotFound(w, r)
	case strings.HasSuffix(path, "/check-runs"):
		write(`{"check_runs":` + or(f.jobs[strings.Split(path, "/")[2]], "[]") + `}`)
	case strings.HasSuffix(path, "/status"):
		write(`{"statuses":` + or(f.statuses[strings.Split(path, "/")[2]], "[]") + `}`)
	case strings.HasPrefix(path, "/git/matching-refs/tags/"):
		write(f.tags)
	case strings.HasPrefix(path, "/releases/tags/"):
		rel, ok := f.releases[strings.TrimPrefix(path, "/releases/tags/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		write(rel)
	default:
		http.NotFound(w, r)
	}
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// acmeCatalog offers acme/tools alone.
func acmeCatalog(t *testing.T) (*Catalog, *fakeSource) {
	t.Helper()
	c := newCatalog(t)
	f, gh := newFakeSource(t)
	c.GitHub = gh
	return c, f
}

func TestCreateOpensAPullRequestAsThePerson(t *testing.T) {
	c, f := acmeCatalog(t)
	golden, err := os.ReadFile("../api/testdata/golden/toolchains/golangci-lint/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}

	res, err := c.Create(as("grant"), "", "", string(golden))
	if err != nil {
		t.Fatal(err)
	}
	cr := res.Created
	if cr == nil || cr.Toolchain != "golangci-lint" || cr.Pull != 7 || cr.Branch != "toolchain/golangci-lint" || !cr.Written || cr.Source != "acme-tools" {
		t.Fatalf("created = %+v (%s)", cr, res.Message)
	}
	if f.branches["toolchain/golangci-lint"] != "base" {
		t.Errorf("branches = %v, want toolchain/golangci-lint from main's base", f.branches)
	}
	if got := f.files["toolchain/golangci-lint:toolchains/golangci-lint/Dockerfile"]; got != string(golden) {
		t.Errorf("committed Dockerfile = %q", got)
	}
	pr := f.pulls[0]
	if pr["title"] != "feat(golangci-lint): add the golangci-lint toolchain" || pr["base"] != "main" ||
		!strings.Contains(pr["body"].(string), "`golangci-lint`: `golangci-lint --version`") {
		t.Errorf("pull request = %+v", pr)
	}

	if _, err := c.Create(as("grant"), "acme/tools", "", string(golden)); err == nil || !strings.Contains(err.Error(), "open pull request, https://github.com/acme/tools/pull/7") {
		t.Errorf("a second create = %v, want the open pull request", err)
	}

	// The pull request closed unmerged, its branch moved on: a new create starts it over from main.
	f.pulls[0]["state"] = "closed"
	f.branches["toolchain/golangci-lint"] = "stale"
	again, err := c.Create(as("grant"), "", "", string(golden))
	if err != nil || again.Created.Pull != 8 || f.branches["toolchain/golangci-lint"] != "base" {
		t.Errorf("create after a closed pull request = %+v, %v; branch at %s", again.Created, err, f.branches["toolchain/golangci-lint"])
	}
}

func TestCreateHoldsAHandWrittenDockerfileToItsName(t *testing.T) {
	c, f := acmeCatalog(t)
	goDockerfile := strings.ReplaceAll(handWritten, `name="go"`, `name="gotools"`)
	helmGolden, err := os.ReadFile("../api/testdata/golden/toolchains/helm/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct{ name, dockerfile, err string }{
		"no name":               {dockerfile: goDockerfile, err: "name the toolchain"},
		"name and label differ": {name: "golang", dockerfile: goDockerfile, err: `"gotools", want "golang"`},
		"the source has it":     {name: "helm", dockerfile: strings.ReplaceAll(handWritten, `name="go"`, `name="helm"`), err: "already has toolchain helm"},
		"not a toolchain name":  {name: "Go", dockerfile: goDockerfile, err: "want [a-z0-9-]"},
		"a written one renamed": {name: "other", dockerfile: string(helmGolden), err: "is toolchain helm, not other"},
		"its own name":          {name: "gotools", dockerfile: goDockerfile},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.Create(as("grant"), "", tc.name, tc.dockerfile)
			if tc.err == "" {
				if err != nil {
					t.Errorf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("err = %v, want %q", err, tc.err)
			}
		})
	}
	if res := f.files["toolchain/gotools:toolchains/gotools/Dockerfile"]; res == "" {
		t.Error("the hand-written gotools was not committed")
	}

	f.push = false
	if _, err := c.Create(as("grant"), "", "jq", strings.ReplaceAll(handWritten, `name="go"`, `name="jq"`)); err == nil || !strings.Contains(err.Error(), "cannot push") {
		t.Errorf("create without push access = %v", err)
	}
	none, err := c.Create(as(""), "", "jq", strings.ReplaceAll(handWritten, `name="go"`, `name="jq"`))
	if err != nil || none.Status != sources.StatusGrantRequired {
		t.Errorf("without the grant = %+v, %v", none, err)
	}
}

func TestBuildStatusFollowsThePullRequestToItsRelease(t *testing.T) {
	c, f := acmeCatalog(t)
	if _, err := c.Create(as("grant"), "", "jq", strings.ReplaceAll(handWritten, `name="go"`, `name="jq"`)); err != nil {
		t.Fatal(err)
	}
	pr := f.pulls[0]
	step := func(want string) *Build {
		t.Helper()
		res, err := c.BuildStatus(as("grant"), "", "jq", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.Build.State != want {
			t.Fatalf("state = %s (%s), want %s", res.Build.State, res.Message, want)
		}
		return res.Build
	}

	step(StateQueued)
	f.jobs["head"] = `[{"name":"Toolchain contract","status":"queued"},{"name":"Lint","status":"completed","conclusion":"skipped"}]`
	step(StateQueued)
	f.jobs["head"] = `[{"name":"Toolchain contract","status":"in_progress","html_url":"https://github.com/acme/tools/actions/runs/1/job/2"}]`
	b := step(StateChecking)
	if len(b.Jobs) != 1 || b.Commit != "head" || b.Pull != 7 {
		t.Errorf("checking = %+v", b)
	}
	f.jobs["head"] = `[{"name":"Toolchain contract","status":"completed","conclusion":"failure","html_url":"https://github.com/acme/tools/actions/runs/1/job/2"}]`
	if b := step(StateFailed); b.Log != "https://github.com/acme/tools/actions/runs/1/job/2" {
		t.Errorf("failed without the job's log: %+v", b)
	}
	f.jobs["head"] = `[{"name":"Toolchain contract","status":"completed","conclusion":"success"}]`
	f.statuses["head"] = `[{"context":"ci/circleci: lint","state":"pending","target_url":"https://circleci.com/1"}]`
	step(StateChecking)
	f.statuses["head"] = `[{"context":"ci/circleci: lint","state":"success"}]`
	step(StateChecked)

	merged := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	pr["state"], pr["merged_at"], pr["merge_commit_sha"] = "closed", merged, "merge"
	step(StateQueued)
	f.jobs["merge"] = `[{"name":"Release","status":"in_progress"}]`
	step(StateBuilding)
	f.tags = `[{"ref":"refs/tags/jq/v0.1.0","object":{"type":"commit","sha":"older"}},{"ref":"refs/tags/jq/v0.2.0","object":{"type":"commit","sha":"merge"}}]`
	step(StateBuilding) // tagged, no release yet
	f.releases["jq/v0.2.0"] = `{"tag_name":"jq/v0.2.0","html_url":"https://github.com/acme/tools/releases/tag/jq/v0.2.0"}`
	images := images{}
	c.Registry = images
	step(StateBuilding) // released, the image not pushed yet
	images["registry.acme.io/acme-tools/jq:0.2.0"] = nil
	step(StateBuilding) // the image pushed, its job still runs
	f.jobs["merge"] = `[{"name":"Release","status":"completed","conclusion":"success"}]`
	b = step(StatePublished)
	if b.Version != "v0.2.0" || b.Image != "registry.acme.io/acme-tools/jq:0.2.0" || b.ReleaseURL == "" {
		t.Errorf("published = %+v", b)
	}

	// A registry that wants credentials: published once every job passed.
	images["registry.acme.io/acme-tools/jq:0.2.0"] = registry.ErrUnknown
	step(StatePublished)
	f.jobs["merge"] = `[{"name":"Release","status":"in_progress"}]`
	step(StateBuilding)
	f.jobs["merge"] = `[{"name":"Release","status":"completed","conclusion":"success"}]`

	f.tags = `[]`
	step(StateUnreleased)
	f.jobs["merge"] = `[{"name":"Release","status":"completed","conclusion":"failure","html_url":"https://github.com/acme/tools/actions/runs/3/job/4"}]`
	step(StateFailed)

	pr["merged_at"] = nil
	step(StateClosed)
}

// images are the image tags that exist, or the error asking for one gives.
type images map[string]error

func (i images) Exists(_ context.Context, image string) (bool, error) {
	err, ok := i[image]
	return ok && err == nil, err
}

func TestBuildStatusWaitsForTheStateToMove(t *testing.T) {
	poll, maxWait := statusPoll, statusMaxWait
	statusPoll, statusMaxWait = 10*time.Millisecond, time.Second
	t.Cleanup(func() { statusPoll, statusMaxWait = poll, maxWait })

	c, f := acmeCatalog(t)
	if _, err := c.Create(as("grant"), "", "jq", strings.ReplaceAll(handWritten, `name="go"`, `name="jq"`)); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		f.mu.Lock()
		f.jobs["head"] = `[{"name":"Toolchain contract","status":"in_progress"}]`
		f.mu.Unlock()
	}()
	res, err := c.BuildStatus(as("grant"), "acme-tools", "jq", 7, true)
	if err != nil || res.Build.State != StateChecking {
		t.Errorf("waited = %+v, %v, want checking", res.Build, err)
	}

	start := time.Now()
	res, err = c.BuildStatus(as("grant"), "", "jq", 0, true)
	if err != nil || res.Build.State != StateChecking || time.Since(start) < 900*time.Millisecond {
		t.Errorf("an unmoved state = %+v, %v after %s, want checking after the wait", res.Build, err, time.Since(start))
	}
}

func TestBuildStatusRefuses(t *testing.T) {
	c, _ := acmeCatalog(t)
	for name, tc := range map[string]struct {
		name string
		pull int
		err  string
	}{
		"no pull request": {name: "jq", err: "no pull request from toolchain/jq"},
		"unknown number":  {name: "jq", pull: 99, err: "no pull request 99"},
		"unnamed":         {err: "name the toolchain"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.BuildStatus(as("grant"), "", tc.name, tc.pull, false); err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("err = %v, want %q", err, tc.err)
			}
		})
	}
	none, err := c.BuildStatus(as(""), "", "jq", 0, false)
	if err != nil || none.Status != sources.StatusGrantRequired {
		t.Errorf("without the grant = %+v, %v", none, err)
	}
	two := newCatalog(t, platform)
	if _, err := two.BuildStatus(as("grant"), "", "jq", 0, false); err == nil || !strings.Contains(err.Error(), "name the source") {
		t.Errorf("two offered sources = %v", err)
	}
}
