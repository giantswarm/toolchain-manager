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
	"sync"
	"time"

	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/identity"
	"github.com/giantswarm/toolchain-manager/internal/sources"
	"github.com/giantswarm/toolchain-manager/internal/toolchain"
	"github.com/giantswarm/toolchain-manager/internal/updates"
)

// Catalog reads the offered sources' published toolchains.
type Catalog struct {
	Sources *sources.Manager
	// GitHub reads a source's releases and Dockerfiles with the person's
	// token.
	GitHub *github.Client
	// Registry tells whether a published version's image exists; nil
	// leaves it to the source's CI jobs.
	Registry ImageChecker
	// Users names the agents and Sessions that use a toolchain, asked as the
	// person (agent-manager through muster); nil serves no deprecation.
	Users UsageReader
	// Updates keeps the outdated marks of the newest offered versions, which
	// the catalog's reads hand it; Updater writes an update's Dockerfile.
	// Either nil serves no updates.
	Updates *updates.Watch
	Updater *updates.Updater
	// Reviews posts a major update's ask to the source's team; nil posts
	// none, and the update waits for an approving review on GitHub.
	Reviews ReviewAsker
	// ApproveTool is approve_update as muster names it, which the ask's
	// Approve calls as the clicking member.
	ApproveTool string

	// declarations caches what a released Dockerfile declares, by
	// repository and tag: a release's tag does not move.
	declarations sync.Map
}

// ImageChecker tells whether an image tag exists: registry.ErrUnknown when
// the registry cannot be asked.
type ImageChecker interface {
	Exists(ctx context.Context, image string) (bool, error)
}

// Toolchain is a published toolchain of an offered source.
type Toolchain struct {
	Name       string `json:"name"`
	Source     string `json:"source"`
	Repository string `json:"repository"`
	// Version is the newest offered version, or the one described.
	Version string `json:"version"`
	// Image is Version's image, <registry>/<name>:X.Y.Z.
	Image       string     `json:"image"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	ReleaseURL  string     `json:"releaseURL,omitempty"`
	// Versions are the published versions' names, newest first: the offered
	// ones, and the deprecated ones too where asked.
	Versions []string `json:"versions"`
	// Published are those versions with their images and tools.
	Published []Version `json:"published"`
	// Deprecated is set when the whole toolchain is deprecated.
	Deprecated *Deprecated `json:"deprecated,omitempty"`
	// Outdated marks Version, the newest offered one, when a pin of it has
	// a newer version: the background job's notification.
	Outdated *updates.Outdated `json:"outdated,omitempty"`

	registry string
	team     string
	channel  string
	releases map[string]github.Release
}

// Version is one published version of a toolchain and what its image
// declares.
type Version struct {
	Version     string     `json:"version"`
	Image       string     `json:"image"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	ReleaseURL  string     `json:"releaseURL,omitempty"`
	// Harnesses and Tools are what the version's Dockerfile labels the
	// image with.
	Harnesses []string `json:"harnesses,omitempty"`
	Tools     []Tool   `json:"tools"`
	// Unreadable says why the version's Dockerfile, and so its tools, could
	// not be read.
	Unreadable string `json:"unreadable,omitempty"`
	// Deprecated is set on a deprecated version.
	Deprecated *Deprecated `json:"deprecated,omitempty"`
}

// Deprecated is who deprecated a toolchain or a version, and why.
type Deprecated struct {
	By     string     `json:"by"`
	At     *time.Time `json:"at,omitempty"`
	Reason string     `json:"reason,omitempty"`
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

// ListOptions narrow list_toolchains.
type ListOptions struct {
	// Source is one source's name or repository.
	Source string
	// Name is one toolchain's name.
	Name string
	// Deprecated lists the deprecated toolchains and versions too, marked.
	Deprecated bool
}

// List returns the published toolchains of the offered sources, sorted by
// name and source, each with its versions and their tools. A deprecated
// toolchain or version is not offered, so it is left out unless
// opts.Deprecated asks for it.
func (c *Catalog) List(ctx context.Context, opts ListOptions) (ListResult, error) {
	res, err := c.published(ctx, opts.Source, strings.TrimSpace(opts.Name))
	if err != nil || res.Status != "" {
		return res.ListResult, err
	}
	if !opts.Deprecated {
		offered := res.Toolchains[:0]
		for _, t := range res.Toolchains {
			if t, ok := t.offered(); ok {
				offered = append(offered, t)
			}
		}
		res.Toolchains = offered
	}
	c.declareAll(ctx, res.Toolchains)
	for i := range res.Toolchains {
		c.watch(ctx, &res.Toolchains[i])
	}
	res.Message = fmt.Sprintf("%d toolchains from %d sources", len(res.Toolchains), res.reached)
	if len(res.Unreachable) > 0 {
		res.Message += fmt.Sprintf("; %d sources unreachable", len(res.Unreachable))
	}
	return res.ListResult, nil
}

// catalogRead is every published toolchain, deprecations marked, and how
// many sources were read.
type catalogRead struct {
	ListResult
	reached int
}

// published reads the offered sources' releases, the ones of source ref or
// of toolchain name when set, and marks the deprecated toolchains and
// versions.
func (c *Catalog) published(ctx context.Context, ref, name string) (catalogRead, error) {
	offered, err := c.offered(ctx, ref)
	if err != nil {
		return catalogRead{}, err
	}
	token, ok := identity.Grant(ctx)
	if !ok {
		return catalogRead{ListResult: ListResult{Answer: c.grantRequired()}}, nil
	}
	deprecations, err := c.deprecations(ctx)
	if err != nil {
		return catalogRead{}, err
	}
	res := catalogRead{ListResult: ListResult{Toolchains: []Toolchain{}}}
	for _, s := range offered {
		releases, err := c.GitHub.Releases(ctx, token, s.Repository)
		switch {
		case errors.Is(err, github.ErrRefused):
			return catalogRead{ListResult: ListResult{Answer: c.grantRequired()}}, nil
		case errors.Is(err, github.ErrNotFound):
			res.Unreachable = append(res.Unreachable, Unreachable{Source: s.Name, Repository: s.Repository,
				Reason: "the toolchain-manager's GitHub App does not reach it with your sign-in: it is not installed there, or you have no access"})
			continue
		case err != nil:
			res.Unreachable = append(res.Unreachable, Unreachable{Source: s.Name, Repository: s.Repository, Reason: err.Error()})
			continue
		}
		res.reached++
		for _, t := range published(s, releases) {
			if name == "" || t.Name == name {
				res.Toolchains = append(res.Toolchains, deprecations.mark(t))
			}
		}
	}
	sort.Slice(res.Toolchains, func(i, j int) bool {
		a, b := res.Toolchains[i], res.Toolchains[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Source < b.Source
	})
	return res, nil
}

// declareAll fills in every listed version's harnesses and tools from its
// released Dockerfile, read a few at a time.
func (c *Catalog) declareAll(ctx context.Context, list []Toolchain) {
	token, _ := identity.Grant(ctx)
	sem := make(chan struct{}, maxConcurrentReads)
	var wg sync.WaitGroup
	for i := range list {
		for j := range list[i].Published {
			wg.Add(1)
			sem <- struct{}{}
			go func(t *Toolchain, v *Version) {
				defer func() { <-sem; wg.Done() }()
				d, err := c.declaration(ctx, token, t.Repository, t.Name, v.Version)
				if err != nil {
					v.Unreadable = err.Error()
					return
				}
				v.Harnesses, v.Tools = d.Harnesses, d.Tools
			}(&list[i], &list[i].Published[j])
		}
	}
	wg.Wait()
}

// maxConcurrentReads bounds the Dockerfiles one list reads at once.
const maxConcurrentReads = 8

// declaration is what the toolchain's Dockerfile at a released version
// declares.
func (c *Catalog) declaration(ctx context.Context, token, repository, name, version string) (Declaration, error) {
	r, err := c.release(ctx, token, repository, name, version)
	return r.Declaration, err
}

// released is what a released Dockerfile declares and, for one the manager
// wrote, its spec.
type released struct {
	Declaration
	spec *toolchain.Spec
}

// release reads the toolchain's Dockerfile at a released version, once per
// tag.
func (c *Catalog) release(ctx context.Context, token, repository, name, version string) (released, error) {
	tag := name + "/" + version
	key := repository + "@" + tag
	if r, ok := c.declarations.Load(key); ok {
		return r.(released), nil
	}
	dockerfile, err := c.GitHub.File(ctx, token, repository, dockerfilePath(name), tag)
	if err != nil {
		return released{}, fmt.Errorf("read %s at %s: %w", dockerfilePath(name), tag, err)
	}
	r := c.remember(key, dockerfile)
	return r, nil
}

// remember caches what a released Dockerfile declares under key.
func (c *Catalog) remember(key string, dockerfile []byte) released {
	r := released{Declaration: Declare(dockerfile)}
	if r.Tools == nil {
		r.Tools = []Tool{}
	}
	if spec, err := toolchain.Read(dockerfile); err == nil {
		r.spec = &spec
	}
	c.declarations.Store(key, r)
	return r
}

func dockerfilePath(name string) string { return "toolchains/" + name + "/Dockerfile" }

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

// Describe describes the published toolchain name at version (the newest
// offered when empty or latest), of the source ref names when two sources
// publish it. A deprecated version is described, marked.
func (c *Catalog) Describe(ctx context.Context, name, ref, version string) (DescribeResult, error) {
	t, answer, err := c.find(ctx, name, ref)
	if err != nil || answer.Status != "" {
		return DescribeResult{Answer: answer}, err
	}
	if v := strings.TrimSpace(version); v != "" && v != "latest" {
		v = normalizeVersion(v)
		if _, ok := t.releases[v]; !ok {
			return DescribeResult{}, fmt.Errorf("toolchain %s has no published version %s; it has %s", name, v, strings.Join(t.versionNames(), ", "))
		}
		t = t.at(v)
	}
	token, _ := identity.Grant(ctx)
	tag := t.Name + "/" + t.Version
	path := dockerfilePath(t.Name)
	dockerfile, err := c.GitHub.File(ctx, token, t.Repository, path, tag)
	if errors.Is(err, github.ErrRefused) {
		return DescribeResult{Answer: c.grantRequired()}, nil
	}
	if err != nil {
		return DescribeResult{}, fmt.Errorf("read %s at %s in %s: %w", path, tag, t.Repository, err)
	}
	_, readErr := toolchain.Read(dockerfile)
	c.remember(t.Repository+"@"+tag, dockerfile)
	c.watch(ctx, &t)
	d := &Description{
		Toolchain:   t,
		Declaration: Declare(dockerfile),
		Written:     readErr == nil,
		Dockerfile:  "https://github.com/" + t.Repository + "/blob/" + tag + "/" + path,
	}
	msg := fmt.Sprintf("%s %s of %s: %d tools, image %s", t.Name, t.Version, t.Repository, len(d.Tools), t.Image)
	if dep := t.deprecated(t.Version); dep != nil {
		msg += fmt.Sprintf("; deprecated by %s, not offered", dep.By)
	}
	return DescribeResult{Toolchain: d, Answer: Answer{Message: msg}}, nil
}

// find is the one published toolchain name, of the source ref names when
// two sources publish it, at its newest offered version (its newest when
// none is offered), deprecated versions included.
func (c *Catalog) find(ctx context.Context, name, ref string) (Toolchain, Answer, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Toolchain{}, Answer{}, errors.New("name the toolchain, as list_toolchains shows it")
	}
	list, err := c.published(ctx, ref, name)
	if err != nil || list.Status != "" {
		return Toolchain{}, list.Answer, err
	}
	switch len(list.Toolchains) {
	case 0:
		msg := fmt.Sprintf("no published toolchain %s: list_toolchains shows them", name)
		if len(list.Unreachable) > 0 {
			msg += fmt.Sprintf(" (%d sources unreachable)", len(list.Unreachable))
		}
		return Toolchain{}, Answer{}, errors.New(msg)
	case 1:
	default:
		names := make([]string, len(list.Toolchains))
		for i, t := range list.Toolchains {
			names[i] = t.Source
		}
		return Toolchain{}, Answer{}, fmt.Errorf("toolchain %s is published by the sources %s: name the source", name, strings.Join(names, " and "))
	}
	t := list.Toolchains[0]
	if o, ok := t.offered(); ok {
		t = t.at(o.Version)
	}
	return t, Answer{}, nil
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
			t = &Toolchain{Name: m[1], Source: s.Name, Repository: s.Repository, registry: s.Registry, team: s.Team, channel: s.SlackChannel, releases: map[string]github.Release{}}
			byName[m[1]] = t
		}
		t.releases[m[2]] = r
	}
	out := make([]Toolchain, 0, len(byName))
	for _, t := range byName {
		names := make([]string, 0, len(t.releases))
		for v := range t.releases {
			names = append(names, v)
		}
		sort.Slice(names, func(i, j int) bool { return newer(names[i], names[j]) })
		for _, v := range names {
			t.Published = append(t.Published, t.version(v))
		}
		t.Versions = names
		out = append(out, t.at(names[0]))
	}
	return out
}

// version is one of the toolchain's published versions, without what its
// image declares.
func (t Toolchain) version(version string) Version {
	r := t.releases[version]
	v := Version{Version: version, Image: t.registry + "/" + t.Name + ":" + strings.TrimPrefix(version, "v"), ReleaseURL: r.HTMLURL, Tools: []Tool{}}
	if !r.PublishedAt.IsZero() {
		p := r.PublishedAt.UTC()
		v.PublishedAt = &p
	}
	return v
}

// at is the toolchain at one of its published versions.
func (t Toolchain) at(version string) Toolchain {
	v := t.version(version)
	t.Version, t.Image, t.ReleaseURL, t.PublishedAt = v.Version, v.Image, v.ReleaseURL, v.PublishedAt
	return t
}

// offered is the toolchain as the installation offers it: its versions
// that are not deprecated, at the newest of them; false when the toolchain
// is deprecated or every version is.
func (t Toolchain) offered() (Toolchain, bool) {
	if t.Deprecated != nil {
		return Toolchain{}, false
	}
	var versions []Version
	for _, v := range t.Published {
		if v.Deprecated == nil {
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		return Toolchain{}, false
	}
	t = t.at(versions[0].Version)
	t.Published = versions
	t.Versions = t.versionNames()
	return t, true
}

// deprecated is the deprecation of version, or of the whole toolchain.
func (t Toolchain) deprecated(version string) *Deprecated {
	if t.Deprecated != nil {
		return t.Deprecated
	}
	for _, v := range t.Published {
		if v.Version == version {
			return v.Deprecated
		}
	}
	return nil
}

func (t Toolchain) versionNames() []string {
	out := make([]string, len(t.Published))
	for i, v := range t.Published {
		out[i] = v.Version
	}
	return out
}

// normalizeVersion is a version as a release tag names it, vX.Y.Z.
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v != "" && !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
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
