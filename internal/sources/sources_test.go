package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/giantswarm/toolchain-manager/api/v1alpha1"
	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/identity"
)

const ns = "agent-platform"

var (
	owner = &identity.Identity{Subject: "o", Email: "owner@example.com", EmailVerified: true, Groups: []string{"platform-admins"}}
	alice = &identity.Identity{Subject: "a", Email: "alice@example.com", EmailVerified: true, Groups: []string{"developers"}}
	bob   = &identity.Identity{Subject: "b", Email: "bob@example.com", EmailVerified: true}
)

// fakeGitHub answers GET /repos/<owner>/<name> for the token "grant": acme/tools
// writable, acme/readonly read-only, any other repository not reached; any
// other token is refused.
func fakeGitHub(t *testing.T) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer grant" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repos/acme/tools":
			_, _ = w.Write([]byte(`{"full_name":"acme/tools","html_url":"https://github.com/acme/tools","permissions":{"push":true}}`))
		case "/repos/acme/readonly":
			_, _ = w.Write([]byte(`{"full_name":"acme/readonly","html_url":"https://github.com/acme/readonly","permissions":{"pull":true}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &github.Client{API: srv.URL, HTTP: srv.Client()}
}

func newManager(t *testing.T, objs ...client.Object) *Manager {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	m := &Manager{
		Client:     fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.ToolchainSource{}).WithObjects(objs...).Build(),
		Namespace:  ns,
		Configured: []Configured{{Repository: "giantswarm/agent-toolchains", Registry: "gsoci.azurecr.io/giantswarm/agent-toolchains"}},
		Owners:     Owners{Groups: []string{"platform-admins"}},
		GitHub:     fakeGitHub(t),
		GrantApp:   "github",
		InstallURL: "https://github.com/apps/toolchain-manager/installations/new",
		Now:        func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) },
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	return m
}

func as(id *identity.Identity, grant string) context.Context {
	return identity.ContextWithGrant(identity.ContextWith(context.Background(), id), grant)
}

var add = AddInput{Repository: "https://github.com/acme/tools.git", Registry: "registry.acme.io/toolchains", SlackChannel: "team-tools"}

func TestAddWaitsForTheOwnersApproval(t *testing.T) {
	m := newManager(t)
	res, err := m.Add(as(alice, "grant"), add)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Source
	if res.Status != "" || s == nil {
		t.Fatalf("not added: %+v", res)
	}
	if s.Name != "acme-tools" || s.Repository != "acme/tools" || s.SlackChannel != "#team-tools" || s.AddedBy != "alice@example.com" {
		t.Errorf("source = %+v", s)
	}
	if s.Phase != v1alpha1.PhasePending || s.Offered {
		t.Errorf("a developer's source is %s, offered %v; want Pending, not offered", s.Phase, s.Offered)
	}

	if _, err := m.Approve(as(alice, ""), "acme/tools"); err == nil || !strings.Contains(err.Error(), "not an owner") {
		t.Errorf("a developer approved: %v", err)
	}
	s2, err := m.Approve(as(owner, ""), "acme-tools")
	if err != nil {
		t.Fatal(err)
	}
	if s2.Phase != v1alpha1.PhaseApproved || !s2.Offered || s2.ApprovedBy != "owner@example.com" || s2.ApprovedAt == nil {
		t.Errorf("approved = %+v", s2)
	}

	list, err := m.List(as(bob, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Origin != OriginConfiguration || !list[0].Offered || list[1].Repository != "acme/tools" || !list[1].Offered {
		t.Errorf("list = %+v", list)
	}
}

func TestAnOwnersSourceIsApprovedByAddingIt(t *testing.T) {
	m := newManager(t)
	res, err := m.Add(as(owner, "grant"), add)
	if err != nil {
		t.Fatal(err)
	}
	if res.Source.Phase != v1alpha1.PhaseApproved || res.Source.ApprovedBy != "owner@example.com" {
		t.Errorf("source = %+v", res.Source)
	}
}

func TestAddNeedsTheGrant(t *testing.T) {
	m := newManager(t)
	for name, tc := range map[string]struct {
		grant, repo, status, err string
	}{
		"no grant in muster": {grant: "", repo: "acme/tools", status: StatusGrantRequired},
		"grant refused":      {grant: "revoked", repo: "acme/tools", status: StatusGrantRequired},
		"App not installed":  {grant: "grant", repo: "acme/other", status: StatusInstallRequired},
		"no push":            {grant: "grant", repo: "acme/readonly", err: "cannot push"},
		"configured already": {grant: "grant", repo: "GiantSwarm/agent-toolchains", err: "a source already"},
		"not a repository":   {grant: "grant", repo: "acme", err: "want owner/name"},
		"another host":       {grant: "grant", repo: "https://gitlab.com/acme/tools", err: "want owner/name"},
	} {
		t.Run(name, func(t *testing.T) {
			in := add
			in.Repository = tc.repo
			res, err := m.Add(as(alice, tc.grant), in)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tc.status || res.Source != nil {
				t.Errorf("result = %+v, want status %s", res, tc.status)
			}
			if tc.status == StatusGrantRequired && (len(res.Apps) != 1 || res.Apps[0] != "github") {
				t.Errorf("apps = %v, want [github]", res.Apps)
			}
			if tc.status == StatusInstallRequired && res.InstallURL != m.InstallURL {
				t.Errorf("installURL = %q", res.InstallURL)
			}
		})
	}
	list, _ := m.List(as(alice, ""))
	if len(list) != 1 {
		t.Errorf("a refused add left a source: %+v", list)
	}
}

func TestAddValidatesItsInput(t *testing.T) {
	m := newManager(t)
	for name, in := range map[string]AddInput{
		"no registry":      {Repository: "acme/tools", SlackChannel: "x"},
		"registry no host": {Repository: "acme/tools", Registry: "toolchains", SlackChannel: "x"},
		"no channel":       {Repository: "acme/tools", Registry: "registry.acme.io/toolchains"},
		"bad channel":      {Repository: "acme/tools", Registry: "registry.acme.io/toolchains", SlackChannel: "#Team Tools"},
	} {
		if _, err := m.Add(as(alice, "grant"), in); err == nil {
			t.Errorf("%s: added", name)
		}
	}
	if _, err := m.Add(context.Background(), add); err == nil || !strings.Contains(err.Error(), "no caller") {
		t.Errorf("no caller: %v", err)
	}
}

func TestRemove(t *testing.T) {
	m := newManager(t)
	if _, err := m.Add(as(alice, "grant"), add); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remove(as(bob, ""), "acme/tools"); err == nil || !strings.Contains(err.Error(), "added by alice@example.com") {
		t.Errorf("bob removed alice's source: %v", err)
	}
	// The same email without the IdP's word for it is not alice.
	mallory := &identity.Identity{Subject: "m", Email: "alice@example.com"}
	if _, err := m.Remove(as(mallory, ""), "acme/tools"); err == nil {
		t.Errorf("an unverified email removed alice's source")
	}
	if _, err := m.Remove(as(owner, ""), "giantswarm-agent-toolchains"); err == nil || !strings.Contains(err.Error(), "configuration") {
		t.Errorf("a configured source removed: %v", err)
	}
	if _, err := m.Remove(as(alice, ""), "acme/nothing"); err == nil || !strings.Contains(err.Error(), "no source") {
		t.Errorf("an unknown source removed: %v", err)
	}
	s, err := m.Remove(as(alice, ""), "ACME/tools")
	if err != nil {
		t.Fatal(err)
	}
	if s.Repository != "acme/tools" {
		t.Errorf("removed %+v", s)
	}
	// An owner removes anyone's.
	if _, err := m.Add(as(alice, "grant"), add); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remove(as(owner, ""), "acme-tools"); err != nil {
		t.Fatal(err)
	}
	list, _ := m.List(as(alice, ""))
	if len(list) != 1 {
		t.Errorf("list after removal = %+v", list)
	}
}

func TestOwners(t *testing.T) {
	o := Owners{Groups: []string{"admins"}, Emails: []string{"Boss@example.com"}}
	for _, tc := range []struct {
		id   *identity.Identity
		want bool
	}{
		{&identity.Identity{Email: "boss@example.com", EmailVerified: true}, true},
		{&identity.Identity{Email: "boss@example.com"}, false},
		{&identity.Identity{Email: "x@example.com", Groups: []string{"admins"}}, true},
		{&identity.Identity{Email: "x@example.com", Groups: []string{"devs"}}, false},
		{nil, false},
	} {
		if got := o.Owns(tc.id); got != tc.want {
			t.Errorf("Owns(%+v) = %v", tc.id, got)
		}
	}
}

func TestValidateRefusesABadConfiguration(t *testing.T) {
	for name, c := range map[string][]Configured{
		"twice":       {{Repository: "a/b", Registry: "r.io/x"}, {Repository: "A/b", Registry: "r.io/x"}},
		"no registry": {{Repository: "a/b"}},
		"no repo":     {{Repository: "a", Registry: "r.io/x"}},
	} {
		if err := (&Manager{Configured: c, GrantApp: "github"}).Validate(); err == nil {
			t.Errorf("%s: valid", name)
		}
	}
}

func TestName(t *testing.T) {
	for in, want := range map[string]string{
		"acme/tools":                   "acme-tools",
		"Acme.Corp/my_tools":           "acme-corp-my-tools",
		"-a-/-b-":                      "a-b",
		strings.Repeat("a", 70) + "/b": strings.Repeat("a", 63),
	} {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
}
