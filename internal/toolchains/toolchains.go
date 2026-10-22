// Package toolchains lists and describes the toolchains an installation
// offers: those its offered sources have published. A source's release
// workflow publishes a toolchain only once it keeps the toolchain contract,
// and records each version as a GitHub Release <name>/vX.Y.Z of the source,
// its image <registry>/<name>:X.Y.Z. The releases and Dockerfiles are read
// with the person's token of the toolchain-manager's GitHub App, so a
// person sees the toolchains of the sources the App's installation grants
// them.
package toolchains

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/identity"
	"github.com/giantswarm/toolchain-manager/internal/sources"
	"github.com/giantswarm/toolchain-manager/internal/toolchain"
)

// Catalog reads the offered sources' published toolchains.
type Catalog struct {
	Sources *sources.Manager
	// GitHub reads a source's releases and Dockerfiles with the person's
	// token.
	GitHub *github.Client
}

// Toolchain is a published toolchain of an offered source.
type Toolchain struct {
	Name       string `json:"name"`
	Source     string `json:"source"`
	Repository string `json:"repository"`
	// Version is the newest published version, or the one described.
	Version string `json:"version"`
	// Image is Version's image, <registry>/<name>:X.Y.Z.
	Image       string     `json:"image"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	ReleaseURL  string     `json:"releaseURL,omitempty"`
	// Versions are the published versions, newest first.
	Versions []string `json:"versions"`

	registry string
	releases map[string]github.Release
}

// Unreachable is an offered source whose toolchains could not be read.
type Unreachable struct {
	Source     string `json:"source"`
	Repository string `json:"repository"`
	Reason     string `json:"reason"`
}

// Answer is what a tool answers when it cannot read without the person:
// Status sources.StatusGrantRequired and the App muster asks them to grant.
type Answer struct {
	Status  string   `json:"status,omitempty"`
	Apps    []string `json:"apps,omitempty"`
	Message string   `json:"message"`
}

// ListResult is list_toolchains' answer.
type ListResult struct {
	Toolchains  []Toolchain   `json:"toolchains"`
	Unreachable []Unreachable `json:"unreachable,omitempty"`
	Answer
}

// List returns the published toolchains of the offered sources, or of the
// one source ref names, sorted by name and source.
func (c *Catalog) List(ctx context.Context, ref string) (ListResult, error) {
	offered, err := c.offered(ctx, ref)
	if err != nil {
		return ListResult{}, err
	}
	token, ok := identity.Grant(ctx)
	if !ok {
		return ListResult{Answer: c.grantRequired()}, nil
	}
	res := ListResult{Toolchains: []Toolchain{}}
	for _, s := range offered {
		releases, err := c.GitHub.Releases(ctx, token, s.Repository)
		switch {
		case errors.Is(err, github.ErrRefused):
			return ListResult{Answer: c.grantRequired()}, nil
		case errors.Is(err, github.ErrNotFound):
			res.Unreachable = append(res.Unreachable, Unreachable{Source: s.Name, Repository: s.Repository,
				Reason: "the toolchain-manager's GitHub App does not reach it with your sign-in: it is not installed there, or you have no access"})
			continue
		case err != nil:
			res.Unreachable = append(res.Unreachable, Unreachable{Source: s.Name, Repository: s.Repository, Reason: err.Error()})
			continue
		}
		res.Toolchains = append(res.Toolchains, published(s, releases)...)
	}
	sort.Slice(res.Toolchains, func(i, j int) bool {
		a, b := res.Toolchains[i], res.Toolchains[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Source < b.Source
	})
	res.Message = fmt.Sprintf("%d toolchains from %d sources", len(res.Toolchains), len(offered)-len(res.Unreachable))
	if len(res.Unreachable) > 0 {
		res.Message += fmt.Sprintf("; %d sources unreachable", len(res.Unreachable))
	}
	return res, nil
}

// Description is describe_toolchain's toolchain: one published version and
// what its image declares.
type Description struct {
	Toolchain
	Declaration
	// Written: the manager wrote the version's Dockerfile, so add_packages
	// and add_binary change it; else it is hand-written.
	Written bool `json:"written"`
	// Dockerfile is the version's Dockerfile in the source.
	Dockerfile string `json:"dockerfile"`
}

// DescribeResult is describe_toolchain's answer.
type DescribeResult struct {
	Toolchain *Description `json:"toolchain,omitempty"`
	Answer
}

// Describe describes the published toolchain name at version (newest when
// empty or latest), of the source ref names when two sources publish it.
func (c *Catalog) Describe(ctx context.Context, name, ref, version string) (DescribeResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return DescribeResult{}, errors.New("name the toolchain, as list_toolchains shows it")
	}
	list, err := c.List(ctx, ref)
	if err != nil || list.Status != "" {
		return DescribeResult{Answer: list.Answer}, err
	}
	var found []Toolchain
	for _, t := range list.Toolchains {
		if t.Name == name {
			found = append(found, t)
		}
	}
	switch len(found) {
	case 0:
		msg := fmt.Sprintf("no published toolchain %s: list_toolchains shows them", name)
		if len(list.Unreachable) > 0 {
			msg += fmt.Sprintf(" (%d sources unreachable)", len(list.Unreachable))
		}
		return DescribeResult{}, errors.New(msg)
	case 1:
	default:
		names := make([]string, len(found))
		for i, t := range found {
			names[i] = t.Source
		}
		return DescribeResult{}, fmt.Errorf("toolchain %s is published by the sources %s: name the source", name, strings.Join(names, " and "))
	}
	t := found[0]
	if v := strings.TrimSpace(version); v != "" && v != "latest" {
		if !strings.HasPrefix(v, "v") {
			v = "v" + v
		}
		if _, ok := t.releases[v]; !ok {
			return DescribeResult{}, fmt.Errorf("toolchain %s has no published version %s; it has %s", name, v, strings.Join(t.Versions, ", "))
		}
		t = t.at(v)
	}
	token, _ := identity.Grant(ctx)
	tag := t.Name + "/" + t.Version
	path := "toolchains/" + t.Name + "/Dockerfile"
	dockerfile, err := c.GitHub.File(ctx, token, t.Repository, path, tag)
	if errors.Is(err, github.ErrRefused) {
		return DescribeResult{Answer: c.grantRequired()}, nil
	}
	if err != nil {
		return DescribeResult{}, fmt.Errorf("read %s at %s in %s: %w", path, tag, t.Repository, err)
	}
	_, readErr := toolchain.Read(dockerfile)
	d := &Description{
		Toolchain:   t,
		Declaration: Declare(dockerfile),
		Written:     readErr == nil,
		Dockerfile:  "https://github.com/" + t.Repository + "/blob/" + tag + "/" + path,
	}
	return DescribeResult{Toolchain: d, Answer: Answer{Message: fmt.Sprintf("%s %s of %s: %d tools, image %s", t.Name, t.Version, t.Repository, len(d.Tools), t.Image)}}, nil
}

// offered returns the offered sources, or the one ref names by name or
// repository.
func (c *Catalog) offered(ctx context.Context, ref string) ([]sources.Source, error) {
	offered, err := c.Sources.Offered(ctx)
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return offered, nil
	}
	repo, _ := sources.NormalizeRepository(ref)
	for _, s := range offered {
		if strings.EqualFold(s.Name, ref) || strings.EqualFold(s.Repository, repo) {
			return []sources.Source{s}, nil
		}
	}
	return nil, fmt.Errorf("no offered source %s: list_sources shows the sources and whether their toolchains are offered", ref)
}

func (c *Catalog) grantRequired() Answer {
	return Answer{
		Status: sources.StatusGrantRequired,
		Apps:   []string{c.Sources.GrantApp},
		Message: fmt.Sprintf("grant the toolchain-manager's GitHub App once in muster (core_auth_login app=%s): the toolchains are read from their sources with your sign-in",
			c.Sources.GrantApp),
	}
}

// releaseTag is a toolchain version's release tag, <name>/vX.Y.Z.
var releaseTag = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*)/(v(\d+)\.(\d+)\.(\d+))$`)

// published groups a source's releases into its toolchains: drafts and
// pre-releases are not published.
func published(s sources.Source, releases []github.Release) []Toolchain {
	byName := map[string]*Toolchain{}
	for _, r := range releases {
		m := releaseTag.FindStringSubmatch(r.TagName)
		if m == nil || r.Draft || r.Prerelease {
			continue
		}
		t, ok := byName[m[1]]
		if !ok {
			t = &Toolchain{Name: m[1], Source: s.Name, Repository: s.Repository, registry: s.Registry, releases: map[string]github.Release{}}
			byName[m[1]] = t
		}
		t.releases[m[2]] = r
		t.Versions = append(t.Versions, m[2])
	}
	out := make([]Toolchain, 0, len(byName))
	for _, t := range byName {
		sort.Slice(t.Versions, func(i, j int) bool { return newer(t.Versions[i], t.Versions[j]) })
		out = append(out, t.at(t.Versions[0]))
	}
	return out
}

// at is the toolchain at one of its published versions.
func (t Toolchain) at(version string) Toolchain {
	r := t.releases[version]
	t.Version = version
	t.Image = t.registry + "/" + t.Name + ":" + strings.TrimPrefix(version, "v")
	t.ReleaseURL = r.HTMLURL
	t.PublishedAt = nil
	if !r.PublishedAt.IsZero() {
		p := r.PublishedAt.UTC()
		t.PublishedAt = &p
	}
	return t
}

// newer compares two vX.Y.Z versions.
func newer(a, b string) bool {
	pa, pb := semver(a), semver(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func semver(v string) [3]int {
	var out [3]int
	for i, p := range strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}
