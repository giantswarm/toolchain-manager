// Package alpine is the Alpine package source: the APKINDEX of each
// configured branch, searched with each package's version per branch,
// resolved to the dependency closure apk installs, and installed into a
// toolchain's prefix with `apk add --root`.
package alpine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/giantswarm/toolchain-manager/internal/packages"
)

// Name is the source's name.
const Name = "alpine"

// Branch is an Alpine branch a toolchain can build on.
type Branch struct {
	// Name is the branch, v3.22.
	Name string
	// Base is the digest-pinned Alpine image of that branch the build stage
	// runs on; its /etc/apk/repositories names the branch's repositories.
	Base string
}

// Config configures the source.
type Config struct {
	// Branches, newest first: the first is the default.
	Branches []Branch
	// Repositories of each branch, main and community by default.
	Repositories []string
	// Arch is the architecture whose index is read, x86_64 by default. The
	// dependencies are the same on every architecture Alpine builds.
	Arch string
	// Harnesses a toolchain from Alpine's packages fits: every musl Harness
	// whose image has a /bin/sh, since the wrappers bring their own loader.
	Harnesses []string
	Fetcher   Fetcher
	// TTL is how long a branch's index is kept, an hour by default.
	TTL time.Duration
}

// DefaultBranches are the branches of the current Harness images, newest
// first: claude-harness is on Alpine 3.24 and codex-harness on 3.22.
var DefaultBranches = []Branch{
	{Name: "v3.24", Base: "docker.io/library/alpine:3.24.3@sha256:b3413572228e34bd1c9245f5565e6cd127005655e8a7899aef33d8e83255c9e6"},
	{Name: "v3.22", Base: "docker.io/library/alpine:3.22.7@sha256:e9f2de928efcc1b53d0948e684b4f490315f280de6044844ec0ff26c0b44d3cf"},
}

// DefaultHarnesses are the Harnesses a toolchain from Alpine's packages fits.
var DefaultHarnesses = []string{"kagent", "claude", "codex", "omp"}

// Source is the Alpine package source.
type Source struct {
	cfg Config

	mu      sync.Mutex
	indexes map[string]cached
	now     func() time.Time
}

type cached struct {
	ix      *index
	fetched time.Time
}

var _ packages.Source = (*Source)(nil)

// New returns the Alpine source for cfg, its defaults filled in.
func New(cfg Config) *Source {
	if len(cfg.Branches) == 0 {
		cfg.Branches = DefaultBranches
	}
	if len(cfg.Repositories) == 0 {
		cfg.Repositories = []string{"main", "community"}
	}
	if cfg.Arch == "" {
		cfg.Arch = "x86_64"
	}
	if len(cfg.Harnesses) == 0 {
		cfg.Harnesses = DefaultHarnesses
	}
	if cfg.Fetcher == nil {
		cfg.Fetcher = HTTPFetcher{}
	}
	if cfg.TTL == 0 {
		cfg.TTL = time.Hour
	}
	return &Source{cfg: cfg, indexes: map[string]cached{}, now: time.Now}
}

// Name is "alpine".
func (s *Source) Name() string { return Name }

// Branches are the configured branches, newest first.
func (s *Source) Branches() []string {
	out := make([]string, len(s.cfg.Branches))
	for i, b := range s.cfg.Branches {
		out[i] = b.Name
	}
	return out
}

// Harnesses are the Harnesses its toolchains fit.
func (s *Source) Harnesses() []string { return append([]string(nil), s.cfg.Harnesses...) }

func (s *Source) branch(name string) (Branch, error) {
	if name == "" {
		return s.cfg.Branches[0], nil
	}
	for _, b := range s.cfg.Branches {
		if b.Name == name {
			return b, nil
		}
	}
	return Branch{}, fmt.Errorf("alpine: no branch %q; the branches are %s", name, strings.Join(s.Branches(), ", "))
}

func (s *Source) index(ctx context.Context, branch string) (*index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.indexes[branch]; ok && s.now().Sub(c.fetched) < s.cfg.TTL {
		return c.ix, nil
	}
	ix := newIndex()
	for _, repo := range s.cfg.Repositories {
		rc, err := s.cfg.Fetcher.Fetch(ctx, branch, repo, s.cfg.Arch)
		if err != nil {
			return nil, fmt.Errorf("alpine %s/%s: %w", branch, repo, err)
		}
		err = parseIndex(ix, repo, rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("alpine %s/%s: %w", branch, repo, err)
		}
	}
	s.indexes[branch] = cached{ix: ix, fetched: s.now()}
	return ix, nil
}

// maxResults bounds a search: a short query matches hundreds of packages.
const maxResults = 25

// Search returns the packages whose name contains query, the exact name
// first, each with its version on every branch.
func (s *Source) Search(ctx context.Context, query string) ([]packages.Package, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, fmt.Errorf("alpine: an empty search")
	}
	found := map[string]*packages.Package{}
	for _, b := range s.cfg.Branches {
		ix, err := s.index(ctx, b.Name)
		if err != nil {
			return nil, err
		}
		for name, r := range ix.packages {
			if !strings.Contains(name, query) {
				continue
			}
			p, ok := found[name]
			if !ok {
				p = &packages.Package{Name: name, Description: r.description, URL: r.url, License: r.license, Repository: r.repository, Versions: map[string]string{}}
				found[name] = p
			}
			p.Versions[b.Name] = r.version
		}
	}
	out := make([]packages.Package, 0, len(found))
	for _, p := range found {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		ei, ej := out[i].Name == query, out[j].Name == query
		if ei != ej {
			return ei
		}
		if len(out[i].Name) != len(out[j].Name) {
			return len(out[i].Name) < len(out[j].Name)
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > maxResults {
		out = out[:maxResults]
	}
	return out, nil
}

// provider picks the package apk installs for a dependency name: the package
// of that name, else the provider of the highest priority, else the first by
// name.
func (ix *index) provider(name string) *record {
	if r, ok := ix.packages[name]; ok {
		return r
	}
	ps := ix.providers[name]
	if len(ps) == 0 {
		return nil
	}
	best := ps[0]
	for _, p := range ps[1:] {
		if p.priority > best.priority || (p.priority == best.priority && p.name < best.name) {
			best = p
		}
	}
	return best
}

// Closure resolves names on branch to the packages apk adds and the shared
// libraries they need. Version constraints are not checked: a branch carries
// one version of each package.
func (s *Source) Closure(ctx context.Context, branch string, names []string) (packages.Closure, error) {
	b, err := s.branch(branch)
	if err != nil {
		return packages.Closure{}, err
	}
	ix, err := s.index(ctx, b.Name)
	if err != nil {
		return packages.Closure{}, err
	}
	type entry struct {
		r          *record
		requested  bool
		requiredBy map[string]bool
	}
	seen := map[string]*entry{}
	libs := map[string]string{}
	unresolved := map[string]bool{}
	var queue []*record
	for _, n := range names {
		r, ok := ix.packages[n]
		if !ok {
			return packages.Closure{}, fmt.Errorf("alpine %s: no package %q", b.Name, n)
		}
		if e, ok := seen[n]; ok {
			e.requested = true
			continue
		}
		seen[n] = &entry{r: r, requested: true, requiredBy: map[string]bool{}}
		queue = append(queue, r)
	}
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		for _, d := range r.depends {
			if strings.HasPrefix(d, "!") {
				continue // a conflict, not a dependency
			}
			n := depName(d)
			p := ix.provider(n)
			if p == nil {
				unresolved[n] = true
				continue
			}
			if strings.HasPrefix(n, "so:") {
				libs[strings.TrimPrefix(n, "so:")] = p.name
			}
			if p.name == r.name {
				continue
			}
			e, ok := seen[p.name]
			if !ok {
				e = &entry{r: p, requiredBy: map[string]bool{}}
				seen[p.name] = e
				queue = append(queue, p)
			}
			e.requiredBy[r.name] = true
		}
	}
	c := packages.Closure{Branch: b.Name}
	for _, e := range seen {
		cp := packages.ClosurePackage{Name: e.r.name, Version: e.r.version, Requested: e.requested, Size: e.r.size}
		for n := range e.requiredBy {
			cp.RequiredBy = append(cp.RequiredBy, n)
		}
		sort.Strings(cp.RequiredBy)
		c.Packages = append(c.Packages, cp)
	}
	sort.Slice(c.Packages, func(i, j int) bool { return c.Packages[i].Name < c.Packages[j].Name })
	for l, p := range libs {
		c.Libraries = append(c.Libraries, packages.Library{Name: l, Provider: p})
	}
	sort.Slice(c.Libraries, func(i, j int) bool { return c.Libraries[i].Name < c.Libraries[j].Name })
	for n := range unresolved {
		c.Unresolved = append(c.Unresolved, n)
	}
	sort.Strings(c.Unresolved)
	return c, nil
}

// versionCommands are the version commands of tools that print theirs
// without --version.
var versionCommands = map[string]string{
	"go":      "go version",
	"gofmt":   "gofmt -l /dev/null",
	"helm":    "helm version --short",
	"kubectl": "kubectl version --client",
	"yamale":  "yamale --help",
}

// packageEnv is the environment the tools of a package, or of a package
// depending on it, need when they run from the prefix through its loader:
// Go and Python find their root from the executable, which is then the
// loader. {prefix} is the toolchain's mount directory.
var packageEnv = map[string]map[string]string{
	"go":      {"GOROOT": "{prefix}/usr/lib/go"},
	"python3": {"PYTHONHOME": "{prefix}/usr"},
}

// Tools are a package's default tools: the command of the package's own
// name when it provides one, else its shortest command (py3-pytest's
// pytest), with the environment the package and its direct dependencies
// need.
func (s *Source) Tools(ctx context.Context, branch, name string) ([]packages.Tool, error) {
	b, err := s.branch(branch)
	if err != nil {
		return nil, err
	}
	ix, err := s.index(ctx, b.Name)
	if err != nil {
		return nil, err
	}
	r, ok := ix.packages[name]
	if !ok {
		return nil, fmt.Errorf("alpine %s: no package %q", b.Name, name)
	}
	var cmds []string
	for _, p := range r.provides {
		if c, ok := strings.CutPrefix(depName(p), "cmd:"); ok {
			if c == name {
				cmds = []string{c}
				break
			}
			cmds = append(cmds, c)
		}
	}
	if len(cmds) == 0 {
		return nil, nil
	}
	sort.Slice(cmds, func(i, j int) bool {
		if len(cmds[i]) != len(cmds[j]) {
			return len(cmds[i]) < len(cmds[j])
		}
		return cmds[i] < cmds[j]
	})
	t := DefaultTool(cmds[0])
	for _, n := range append([]string{name}, r.depends...) {
		for k, v := range packageEnv[depName(n)] {
			if t.Env == nil {
				t.Env = map[string]string{}
			}
			t.Env[k] = v
		}
	}
	return []packages.Tool{t}, nil
}

// DefaultTool is the tool of a command: its version command.
func DefaultTool(cmd string) packages.Tool {
	t := packages.Tool{Name: cmd, Version: cmd + " --version"}
	if v, ok := versionCommands[cmd]; ok {
		t.Version = v
	}
	return t
}

// Install installs pins under prefix with `apk add --root`, musl's loader
// always among them since the wrappers run each tool through it. apk's lock
// is readable by root only, so it is removed: uid 65532 must read every
// file. Packages are built for /, so an absolute symlink (usr/bin/go ->
// /usr/lib/go/bin/go) is pointed into the prefix. --no-scripts: a package's
// scripts would run in a root without a shell.
func (s *Source) Install(branch, prefix string, pins []packages.Pin) (packages.Install, error) {
	b, err := s.branch(branch)
	if err != nil {
		return packages.Install{}, err
	}
	if len(pins) == 0 {
		return packages.Install{Base: b.Base}, nil
	}
	args := []string{"musl"}
	for _, p := range pins {
		if p.Version == "" {
			return packages.Install{}, fmt.Errorf("alpine: %s is not pinned to a version", p.Name)
		}
		args = append(args, p.String())
	}
	run := fmt.Sprintf(`mkdir -p %[1]s \
 && apk add --no-cache --no-scripts --root %[1]s --initdb \
      --keys-dir /etc/apk/keys --repositories-file /etc/apk/repositories \
      %[2]s \
 && rm -f %[1]s/lib/apk/db/lock \
 && find %[1]s -type l | while read -r link; do \
      target=$(readlink "$link"); \
      case "$target" in /*) ln -sfn "%[1]s${target}" "$link" ;; esac; \
    done`, prefix, strings.Join(args, " \\\n      "))
	return packages.Install{Base: b.Base, Run: run}, nil
}
