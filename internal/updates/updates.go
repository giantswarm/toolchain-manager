// Package updates keeps toolchains current: it reads the newest version a
// package source and a binary's GitHub releases offer for every pin of a
// Dockerfile the manager wrote, names the outdated ones with their new
// versions, and writes the toolchain's Dockerfile with the new pins. An
// update changes versions only: the toolchain's tools, Harnesses and
// declared egress stay as they are.
package updates

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/giantswarm/toolchain-manager/internal/binaries"
	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/packages"
	"github.com/giantswarm/toolchain-manager/internal/toolchain"
)

// Kinds of a pin.
const (
	KindPackage = "package"
	KindBinary  = "binary"
)

// Change is an outdated pin and the version it updates to.
type Change struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
	// Major: the major version changes (or a version is no semantic
	// version), so the owning team approves the update.
	Major bool `json:"major,omitempty"`
}

func (c Change) String() string { return fmt.Sprintf("%s %s → %s", c.Name, c.From, c.To) }

// Status is what a check of a toolchain's pins found.
type Status struct {
	// Outdated are the pins with a newer version, packages first, each
	// group by name.
	Outdated []Change `json:"outdated"`
	// Unchecked are the pins whose newest version could not be read, each
	// with why.
	Unchecked []string `json:"unchecked,omitempty"`
}

// BinaryResolver pins a binary by version and checksum.
type BinaryResolver interface {
	Resolve(ctx context.Context, req binaries.Request) (binaries.Binary, error)
}

// Updater checks and updates the pins of a Dockerfile the manager wrote.
type Updater struct {
	Packages *packages.Registry
	Writer   toolchain.Writer
	Binaries BinaryResolver
	// GitHub reads a binary's latest release; Token authenticates it, empty
	// asks anonymously.
	GitHub *github.Client
	Token  string
}

// Check reads the newest version of every pin of spec.
func (u *Updater) Check(ctx context.Context, spec toolchain.Spec) (Status, error) {
	st := Status{Outdated: []Change{}}
	if len(spec.Packages) > 0 {
		src, err := u.Packages.Get(spec.Source)
		if err != nil {
			return Status{}, err
		}
		for _, p := range spec.Packages {
			latest, err := packageVersion(ctx, src, spec.Branch, p.Name)
			if err != nil {
				st.Unchecked = append(st.Unchecked, fmt.Sprintf("package %s: %v", p.Name, err))
				continue
			}
			if latest != p.Version {
				st.Outdated = append(st.Outdated, Change{Kind: KindPackage, Name: p.Name, From: p.Version, To: latest, Major: major(p.Version, latest)})
			}
		}
	}
	for _, b := range spec.Binaries {
		if !strings.Contains(b.From, "/") || strings.HasPrefix(b.From, "https://") {
			st.Unchecked = append(st.Unchecked, fmt.Sprintf("binary %s: a URL download has no release to read its newest version from", b.Name))
			continue
		}
		r, err := u.GitHub.LatestRelease(ctx, u.Token, b.From)
		if err != nil {
			st.Unchecked = append(st.Unchecked, fmt.Sprintf("binary %s: the latest release of %s: %v", b.Name, b.From, err))
			continue
		}
		if r.TagName != b.Version {
			st.Outdated = append(st.Outdated, Change{Kind: KindBinary, Name: b.Name, From: b.Version, To: r.TagName, Major: major(b.Version, r.TagName)})
		}
	}
	sort.SliceStable(st.Outdated, func(i, j int) bool {
		a, b := st.Outdated[i], st.Outdated[j]
		if a.Kind != b.Kind {
			return a.Kind == KindPackage
		}
		return a.Name < b.Name
	})
	return st, nil
}

func packageVersion(ctx context.Context, src packages.Source, branch, name string) (string, error) {
	found, err := src.Search(ctx, name)
	if err != nil {
		return "", err
	}
	for _, p := range found {
		if p.Name != name {
			continue
		}
		if v, ok := p.Versions[branch]; ok {
			return v, nil
		}
		return "", fmt.Errorf("%s %s no longer carries it", src.Name(), branch)
	}
	return "", fmt.Errorf("%s no longer has it", src.Name())
}

// Plan is an update: the changes it makes and the toolchain's Dockerfile
// with them.
type Plan struct {
	Changes    []Change       `json:"changes"`
	Spec       toolchain.Spec `json:"spec"`
	Dockerfile []byte         `json:"-"`
}

// Major reports whether any change is a major one.
func (p Plan) Major() bool {
	return slices.ContainsFunc(p.Changes, func(c Change) bool { return c.Major })
}

// ErrUpToDate is a plan with nothing to update.
var ErrUpToDate = errors.New("up to date")

// Plan checks spec and writes its Dockerfile with the new pins: every
// outdated one, or those only names. The build stage's base is re-pinned to
// the package source's for the branch.
func (u *Updater) Plan(ctx context.Context, spec toolchain.Spec, only []string) (Plan, error) {
	st, err := u.Check(ctx, spec)
	if err != nil {
		return Plan{}, err
	}
	changes := st.Outdated
	if len(only) > 0 {
		changes = nil
		for _, name := range only {
			i := slices.IndexFunc(st.Outdated, func(c Change) bool { return c.Name == strings.TrimSpace(name) })
			if i < 0 {
				return Plan{}, fmt.Errorf("%s is not an outdated pin of toolchain %s%s", name, spec.Name, outdatedHint(st))
			}
			changes = append(changes, st.Outdated[i])
		}
	}
	if len(changes) == 0 {
		if len(st.Unchecked) > 0 {
			return Plan{}, fmt.Errorf("%w; unchecked: %s", ErrUpToDate, strings.Join(st.Unchecked, "; "))
		}
		return Plan{}, ErrUpToDate
	}
	next := spec
	next.Base = ""
	next.Packages = append([]packages.Pin(nil), spec.Packages...)
	next.Binaries = append([]binaries.Binary(nil), spec.Binaries...)
	for _, c := range changes {
		switch c.Kind {
		case KindPackage:
			for i := range next.Packages {
				if next.Packages[i].Name == c.Name {
					next.Packages[i].Version = c.To
				}
			}
		case KindBinary:
			for i := range next.Binaries {
				if next.Binaries[i].Name != c.Name {
					continue
				}
				b, err := u.rebin(ctx, next.Binaries[i], c.To)
				if err != nil {
					return Plan{}, err
				}
				next.Binaries[i] = b
			}
		}
	}
	dockerfile, err := u.Writer.Write(next)
	if err != nil {
		return Plan{}, err
	}
	if next, err = toolchain.Read(dockerfile); err != nil {
		return Plan{}, err
	}
	return Plan{Changes: changes, Spec: next, Dockerfile: dockerfile}, nil
}

func outdatedHint(st Status) string {
	if len(st.Outdated) == 0 {
		return ": it is up to date"
	}
	names := make([]string, len(st.Outdated))
	for i, c := range st.Outdated {
		names[i] = c.Name
	}
	return "; outdated: " + strings.Join(names, ", ")
}

// rebin resolves binary b at the release tag: its asset is named as at its
// pinned release, the versions and architecture in the name replaced.
func (u *Updater) rebin(ctx context.Context, b binaries.Binary, tag string) (binaries.Binary, error) {
	if len(b.Assets) == 0 {
		return binaries.Binary{}, fmt.Errorf("binary %s has no asset", b.Name)
	}
	a := b.Assets[0]
	name := a.URL[strings.LastIndex(a.URL, "/")+1:]
	asset := templated(name, b.Version, a.Arch)
	if asset == name {
		return binaries.Binary{}, fmt.Errorf("binary %s: its asset %s names neither its version %s nor its architecture, so its asset at %s is unknown: add_binary pins it", b.Name, name, b.Version, tag)
	}
	arches := make([]string, len(b.Assets))
	for i, a := range b.Assets {
		arches[i] = a.Arch
	}
	return u.Binaries.Resolve(ctx, binaries.Request{
		Name: b.Name, Repository: b.From, Version: tag, Asset: asset,
		Path: templatedPath(b.Path, b.Version, tag), Arches: arches,
	})
}

var machine = map[string]string{"amd64": "x86_64", "arm64": "aarch64"}

// templated is an asset name as binaries.Request's template: the tag
// {tag}, the version {version}, the architecture {arch} or {machine}.
func templated(name, version, arch string) string {
	out := name
	if strings.HasPrefix(version, "v") {
		out = strings.ReplaceAll(out, version, "{tag}")
	}
	if v := strings.TrimPrefix(version, "v"); v != "" {
		out = strings.ReplaceAll(out, v, "{version}")
	}
	out = strings.ReplaceAll(out, machine[arch], "{machine}")
	return strings.ReplaceAll(out, arch, "{arch}")
}

// templatedPath is an archive member's path at the new tag.
func templatedPath(path, from, to string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(from, "v") {
		path = strings.ReplaceAll(path, from, to)
	}
	return strings.ReplaceAll(path, strings.TrimPrefix(from, "v"), strings.TrimPrefix(to, "v"))
}

// major reports whether from and to differ in their major version: the
// leading number of each, after a prefix such as v or jq-. A version
// without one is taken as major.
func major(from, to string) bool {
	a, okA := leadingNumber(from)
	b, okB := leadingNumber(to)
	return !okA || !okB || a != b
}

func leadingNumber(v string) (string, bool) {
	i := strings.IndexFunc(v, unicode.IsDigit)
	if i < 0 {
		return "", false
	}
	j := i
	for j < len(v) && unicode.IsDigit(rune(v[j])) {
		j++
	}
	return strings.TrimLeft(v[i:j], "0") + "0", true
}
