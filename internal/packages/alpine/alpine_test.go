package alpine

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/toolchain-manager/internal/packages"
)

// recorded is the source over the indexes hack/record-apkindex recorded.
func recorded() *Source {
	return New(Config{Fetcher: DirFetcher("testdata/apkindex")})
}

func TestSearchHelmVersionsPerBranch(t *testing.T) {
	found, err := recorded().Search(context.Background(), "helm")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 || found[0].Name != "helm" {
		t.Fatalf("first result %v, want helm", found)
	}
	want := map[string]string{"v3.24": "3.19.0-r9", "v3.22": "3.18.4-r5"}
	for branch, v := range want {
		if got := found[0].Versions[branch]; got != v {
			t.Errorf("helm on %s: %q, want %q", branch, got, v)
		}
	}
	for _, p := range found {
		if !strings.Contains(p.Name, "helm") {
			t.Errorf("%s does not match helm", p.Name)
		}
	}
}

func TestClosure(t *testing.T) {
	ctx := context.Background()
	src := recorded()

	helm, err := src.Closure(ctx, "v3.24", []string{"helm"})
	if err != nil {
		t.Fatal(err)
	}
	if names := closureNames(helm); !slices.Equal(names, []string{"helm", "musl"}) {
		t.Errorf("helm's closure %v, want helm and musl", names)
	}
	if len(helm.Libraries) != 1 || helm.Libraries[0] != (packages.Library{Name: "libc.musl-x86_64.so.1", Provider: "musl"}) {
		t.Errorf("helm's libraries %v, want musl's libc", helm.Libraries)
	}

	jq, err := src.Closure(ctx, "v3.22", []string{"jq"})
	if err != nil {
		t.Fatal(err)
	}
	if names := closureNames(jq); !slices.Equal(names, []string{"jq", "musl", "oniguruma"}) {
		t.Errorf("jq's closure %v, want jq, musl and oniguruma", names)
	}
	for _, p := range jq.Packages {
		if p.Name == "oniguruma" && (!slices.Equal(p.RequiredBy, []string{"jq"}) || p.Requested) {
			t.Errorf("oniguruma %+v, want required by jq and not requested", p)
		}
	}

	py, err := src.Closure(ctx, "v3.22", []string{"py3-pytest"})
	if err != nil {
		t.Fatal(err)
	}
	names := closureNames(py)
	for _, want := range []string{"py3-pytest", "python3", "py3-pluggy", "libffi", "sqlite-libs", "musl"} {
		if !slices.Contains(names, want) {
			t.Errorf("py3-pytest's closure %v lacks %s", names, want)
		}
	}
	if len(py.Unresolved) != 0 {
		t.Errorf("py3-pytest: unresolved %v", py.Unresolved)
	}
}

func TestClosureRefusesAnUnknownPackageOrBranch(t *testing.T) {
	src := recorded()
	if _, err := src.Closure(context.Background(), "v3.24", []string{"no-such-package"}); err == nil {
		t.Error("an unknown package resolved")
	}
	if _, err := src.Closure(context.Background(), "v2.0", []string{"helm"}); err == nil {
		t.Error("an unknown branch resolved")
	}
}

func TestTools(t *testing.T) {
	ctx := context.Background()
	src := recorded()
	tools, err := src.Tools(ctx, "v3.24", "go")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "go" || tools[0].Version != "go version" || tools[0].Env["GOROOT"] != "{prefix}/usr/lib/go" {
		t.Errorf("go's tools %+v", tools)
	}
	tools, err = src.Tools(ctx, "v3.24", "py3-pytest")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	if !slices.Contains(names, "pytest") {
		t.Errorf("py3-pytest's tools %v lack pytest", names)
	}
}

func TestInstall(t *testing.T) {
	in, err := recorded().Install("v3.22", "/opt/kagent/tools/jq", []packages.Pin{{Name: "jq", Version: "1.8.2-r0"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(in.Base, "docker.io/library/alpine:3.22.") || !strings.Contains(in.Base, "@sha256:") {
		t.Errorf("base %q, want a digest-pinned Alpine 3.22", in.Base)
	}
	for _, want := range []string{"--root /opt/kagent/tools/jq", "jq=1.8.2-r0", "musl", "rm -f /opt/kagent/tools/jq/lib/apk/db/lock"} {
		if !strings.Contains(in.Run, want) {
			t.Errorf("install %q lacks %q", in.Run, want)
		}
	}
	if _, err := recorded().Install("v3.22", "/p", []packages.Pin{{Name: "jq"}}); err == nil {
		t.Error("an unpinned package installed")
	}
}

func closureNames(c packages.Closure) []string {
	var names []string
	for _, p := range c.Packages {
		names = append(names, p.Name)
	}
	return names
}
