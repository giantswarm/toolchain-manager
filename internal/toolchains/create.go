package toolchains

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/identity"
	"github.com/giantswarm/toolchain-manager/internal/sources"
	"github.com/giantswarm/toolchain-manager/internal/toolchain"
)

// labelName is the toolchain's name label, which the contract check holds
// to its directory.
const labelName = labelPrefix + "name"

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Branch is the branch create_toolchain opens a toolchain's pull request
// from, which toolchain_build_status follows.
func Branch(name string) string { return "toolchain/" + name }

// Created is create_toolchain's pull request.
type Created struct {
	Toolchain  string `json:"toolchain"`
	Source     string `json:"source"`
	Repository string `json:"repository"`
	Pull       int    `json:"pull"`
	PullURL    string `json:"pullURL"`
	Branch     string `json:"branch"`
	Path       string `json:"path"`
	// Written: the manager wrote the Dockerfile; else it is hand-written.
	Written bool `json:"written"`
}

// CreateResult is create_toolchain's answer.
type CreateResult struct {
	Created *Created `json:"created,omitempty"`
	Answer
}

// Create opens a pull request, as the person, that adds the toolchain
// dockerfile declares to the offered source ref names: its Dockerfile at
// toolchains/<name>/Dockerfile on the branch toolchain/<name>. A Dockerfile
// the manager wrote names its toolchain; a hand-written one is held to
// name by its io.giantswarm.toolchain.name label. A toolchain the source
// already has, or one with an open pull request, is refused.
func (c *Catalog) Create(ctx context.Context, ref, name, dockerfile string) (CreateResult, error) {
	name = strings.TrimSpace(name)
	written := true
	spec, err := toolchain.Read([]byte(dockerfile))
	switch {
	case errors.Is(err, toolchain.ErrHandWritten) && name == "":
		return CreateResult{}, errors.New("name the toolchain: a hand-written Dockerfile does not name it")
	case errors.Is(err, toolchain.ErrHandWritten):
		written = false
	case err != nil:
		return CreateResult{}, err
	case name == "":
		name = spec.Name
	case name != spec.Name:
		return CreateResult{}, fmt.Errorf("the Dockerfile is toolchain %s, not %s", spec.Name, name)
	}
	if !namePattern.MatchString(name) {
		return CreateResult{}, fmt.Errorf("toolchain name %q: want [a-z0-9-], the directory toolchains/<name>", name)
	}
	if declared := finalLabels(dockerfile)[labelName]; declared != name {
		return CreateResult{}, fmt.Errorf("the Dockerfile's final stage labels %s %q, want %q: the contract check holds it to the toolchain's directory", labelName, declared, name)
	}
	src, err := c.one(ctx, ref)
	if err != nil {
		return CreateResult{}, err
	}
	token, ok := identity.Grant(ctx)
	if !ok {
		return CreateResult{Answer: c.grantRequired()}, nil
	}
	created, err := c.open(ctx, token, src, name, dockerfile)
	if errors.Is(err, github.ErrRefused) {
		return CreateResult{Answer: c.grantRequired()}, nil
	}
	if err != nil {
		return CreateResult{}, err
	}
	created.Written = written
	return CreateResult{Created: created, Answer: Answer{Message: fmt.Sprintf(
		"opened %s for toolchain %s in %s: its CI checks the contract, and its merge publishes %s/%s; toolchain_build_status follows it",
		created.PullURL, name, src.Repository, src.Registry, name)}}, nil
}

func (c *Catalog) open(ctx context.Context, token string, src sources.Source, name, dockerfile string) (*Created, error) {
	repo, err := c.GitHub.Repository(ctx, token, src.Repository)
	if errors.Is(err, github.ErrNotFound) {
		return nil, fmt.Errorf("the toolchain-manager's GitHub App does not reach %s with your sign-in: it is not installed there, or you have no access", src.Repository)
	}
	if err != nil {
		return nil, err
	}
	if !repo.Writable() {
		return nil, fmt.Errorf("you cannot push to %s: a toolchain's pull request is opened as you", src.Repository)
	}
	path := "toolchains/" + name + "/Dockerfile"
	switch _, err := c.GitHub.File(ctx, token, src.Repository, path, repo.DefaultBranch); {
	case err == nil:
		return nil, fmt.Errorf("%s already has toolchain %s (%s on %s): describe_toolchain shows it", src.Repository, name, path, repo.DefaultBranch)
	case !errors.Is(err, github.ErrNotFound):
		return nil, err
	}
	base, err := c.GitHub.Branch(ctx, token, src.Repository, repo.DefaultBranch)
	if err != nil {
		return nil, fmt.Errorf("read %s's %s: %w", src.Repository, repo.DefaultBranch, err)
	}
	branch := Branch(name)
	_, err = c.GitHub.Branch(ctx, token, src.Repository, branch)
	exists := err == nil
	if err != nil && !errors.Is(err, github.ErrNotFound) {
		return nil, err
	}
	if exists {
		prs, err := c.GitHub.PullsFrom(ctx, token, src.Repository, branch)
		if err != nil {
			return nil, err
		}
		for _, pr := range prs {
			if pr.State == "open" {
				return nil, fmt.Errorf("toolchain %s has an open pull request, %s: toolchain_build_status follows it", name, pr.HTMLURL)
			}
		}
	}
	// A branch left by a closed pull request starts over from the base.
	if err := c.GitHub.SetBranch(ctx, token, src.Repository, branch, base, !exists); err != nil {
		return nil, fmt.Errorf("branch %s: %w", branch, err)
	}
	title := fmt.Sprintf("feat(%s): add the %s toolchain", name, name)
	if err := c.GitHub.PutFile(ctx, token, src.Repository, branch, path, title, []byte(dockerfile)); err != nil {
		return nil, fmt.Errorf("commit %s: %w", path, err)
	}
	pr, err := c.GitHub.CreatePull(ctx, token, src.Repository, branch, repo.DefaultBranch, title, pullBody(name, src, dockerfile))
	if err != nil {
		return nil, fmt.Errorf("open the pull request: %w", err)
	}
	return &Created{
		Toolchain: name, Source: src.Name, Repository: src.Repository,
		Pull: pr.Number, PullURL: pr.HTMLURL, Branch: branch, Path: path,
	}, nil
}

// pullBody describes the toolchain the pull request adds.
func pullBody(name string, src sources.Source, dockerfile string) string {
	d := Declare([]byte(dockerfile))
	var b strings.Builder
	fmt.Fprintf(&b, "Adds the `%s` toolchain, opened with the toolchain-manager's `create_toolchain`.\n\n", name)
	fmt.Fprintf(&b, "- Harnesses: %s\n", strings.Join(d.Harnesses, ", "))
	for _, t := range d.Tools {
		fmt.Fprintf(&b, "- `%s`: `%s`\n", t.Name, t.Version)
	}
	if len(d.Egress) > 0 {
		fmt.Fprintf(&b, "- Egress: %s\n", strings.Join(d.Egress, ", "))
	}
	fmt.Fprintf(&b, "\nThe toolchain contract check verifies it on this pull request; its merge publishes `%s/%s`.\n", src.Registry, name)
	return b.String()
}

// one returns the offered source ref names, or the only offered one.
func (c *Catalog) one(ctx context.Context, ref string) (sources.Source, error) {
	offered, err := c.offered(ctx, ref)
	if err != nil {
		return sources.Source{}, err
	}
	switch len(offered) {
	case 0:
		return sources.Source{}, errors.New("the installation offers no source: add_source adds one")
	case 1:
		return offered[0], nil
	}
	names := make([]string, len(offered))
	for i, s := range offered {
		names[i] = s.Name
	}
	return sources.Source{}, fmt.Errorf("name the source, one of %s", strings.Join(names, ", "))
}
