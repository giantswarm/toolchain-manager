// Package api is the toolchain-manager's MCP seam: the tools that search a
// package source, write a toolchain's Dockerfile from picked packages and
// binaries, and render a new toolchain source.
package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/giantswarm/toolchain-manager/internal/binaries"
	"github.com/giantswarm/toolchain-manager/internal/packages"
	"github.com/giantswarm/toolchain-manager/internal/source"
	"github.com/giantswarm/toolchain-manager/internal/toolchain"
)

// BinaryResolver pins a binary by version and checksum.
type BinaryResolver interface {
	Resolve(ctx context.Context, req binaries.Request) (binaries.Binary, error)
}

// Service holds what the tools use.
type Service struct {
	Sources  *packages.Registry
	Writer   toolchain.Writer
	Binaries BinaryResolver
	// DefaultSource is the package source a tool uses when none is named.
	DefaultSource string
}

// SearchResult is search_packages' answer.
type SearchResult struct {
	Source   string             `json:"source"`
	Branches []string           `json:"branches"`
	Packages []packages.Package `json:"packages"`
	// Closures are, for a package named exactly as the query, its dependency
	// closure on every branch that carries it.
	Closures map[string]packages.Closure `json:"closures,omitempty"`
}

// Search searches a package source.
func (s *Service) Search(ctx context.Context, sourceName, query string) (SearchResult, error) {
	src, err := s.source(sourceName)
	if err != nil {
		return SearchResult{}, err
	}
	found, err := src.Search(ctx, query)
	if err != nil {
		return SearchResult{}, err
	}
	res := SearchResult{Source: src.Name(), Branches: src.Branches(), Packages: found}
	if len(found) > 0 && found[0].Name == strings.TrimSpace(query) {
		res.Closures = map[string]packages.Closure{}
		for _, b := range src.Branches() {
			if _, ok := found[0].Versions[b]; !ok {
				continue
			}
			c, err := src.Closure(ctx, b, []string{found[0].Name})
			if err != nil {
				return SearchResult{}, err
			}
			res.Closures[b] = c
		}
	}
	return res, nil
}

// ToolchainResult is the answer of the tools that write a Dockerfile.
type ToolchainResult struct {
	Toolchain string `json:"toolchain"`
	// Path is the Dockerfile's path in a source.
	Path       string `json:"path"`
	Dockerfile string `json:"dockerfile"`
	// Spec is what the Dockerfile is made of, as its spec line holds it.
	Spec toolchain.Spec `json:"spec"`
	// Closure is everything the toolchain's packages install.
	Closure *packages.Closure `json:"closure,omitempty"`
}

// Target names the toolchain a write tool changes: an existing Dockerfile,
// or a new toolchain's name, source, branch and Harnesses.
type Target struct {
	Name       string
	Dockerfile string
	Source     string
	Branch     string
	Harnesses  []string
}

// spec returns the toolchain the target names.
func (s *Service) spec(t Target) (toolchain.Spec, packages.Source, error) {
	if strings.TrimSpace(t.Dockerfile) != "" {
		spec, err := toolchain.Read([]byte(t.Dockerfile))
		if errors.Is(err, toolchain.ErrHandWritten) {
			return toolchain.Spec{}, nil, fmt.Errorf("%w; change it by hand, the contract check verifies it as any other", err)
		}
		if err != nil {
			return toolchain.Spec{}, nil, err
		}
		if t.Name != "" && t.Name != spec.Name {
			return toolchain.Spec{}, nil, fmt.Errorf("the Dockerfile is toolchain %s, not %s", spec.Name, t.Name)
		}
		if t.Source != "" && t.Source != spec.Source {
			return toolchain.Spec{}, nil, fmt.Errorf("toolchain %s builds from the %s source, not %s", spec.Name, spec.Source, t.Source)
		}
		if t.Branch != "" && t.Branch != spec.Branch {
			return toolchain.Spec{}, nil, fmt.Errorf("toolchain %s builds on %s, not %s", spec.Name, spec.Branch, t.Branch)
		}
		if len(t.Harnesses) > 0 {
			spec.Harnesses = t.Harnesses
		}
		src, err := s.source(spec.Source)
		return spec, src, err
	}
	if t.Name == "" {
		return toolchain.Spec{}, nil, errors.New("name a new toolchain or pass the Dockerfile of an existing one")
	}
	src, err := s.source(t.Source)
	if err != nil {
		return toolchain.Spec{}, nil, err
	}
	branch := t.Branch
	if branch == "" {
		branch = src.Branches()[0]
	}
	harnesses := t.Harnesses
	if len(harnesses) == 0 {
		harnesses = src.Harnesses()
	}
	return toolchain.Spec{Name: t.Name, Source: src.Name(), Branch: branch, Harnesses: harnesses}, src, nil
}

// AddPackages adds packages (name or name=version) to the target's
// toolchain; tools, when given, are declared instead of the packages'
// default tools.
func (s *Service) AddPackages(ctx context.Context, t Target, pkgs []string, tools []packages.Tool) (ToolchainResult, error) {
	spec, src, err := s.spec(t)
	if err != nil {
		return ToolchainResult{}, err
	}
	if len(pkgs) == 0 {
		return ToolchainResult{}, errors.New("no package to add")
	}
	names := make([]string, 0, len(pkgs))
	want := map[string]string{}
	for _, p := range pkgs {
		name, version, _ := strings.Cut(strings.TrimSpace(p), "=")
		names = append(names, name)
		want[name] = version
	}
	c, err := src.Closure(ctx, spec.Branch, names)
	if err != nil {
		return ToolchainResult{}, err
	}
	have := map[string]string{}
	for _, p := range c.Packages {
		have[p.Name] = p.Version
	}
	pins := make([]packages.Pin, 0, len(names))
	for _, n := range names {
		v := want[n]
		if v == "" {
			v = have[n]
		} else if v != have[n] {
			return ToolchainResult{}, fmt.Errorf("%s %s carries %s %s, not %s", src.Name(), spec.Branch, n, have[n], v)
		}
		pins = append(pins, packages.Pin{Name: n, Version: v})
	}
	if len(tools) == 0 {
		for _, n := range names {
			ts, err := src.Tools(ctx, spec.Branch, n)
			if err != nil {
				return ToolchainResult{}, err
			}
			tools = append(tools, ts...)
		}
	}
	spec = toolchain.AddPackages(spec, pins, tools)
	all := make([]string, 0, len(spec.Packages))
	for _, p := range spec.Packages {
		all = append(all, p.Name)
	}
	whole, err := src.Closure(ctx, spec.Branch, all)
	if err != nil {
		return ToolchainResult{}, err
	}
	res, err := s.result(spec)
	if err != nil {
		return ToolchainResult{}, err
	}
	res.Closure = &whole
	return res, nil
}

// AddBinary adds a release binary, pinned by version and checksum, to the
// target's toolchain, declared as tool (its name the binary's).
func (s *Service) AddBinary(ctx context.Context, t Target, req binaries.Request, tool packages.Tool) (ToolchainResult, error) {
	spec, _, err := s.spec(t)
	if err != nil {
		return ToolchainResult{}, err
	}
	b, err := s.Binaries.Resolve(ctx, req)
	if err != nil {
		return ToolchainResult{}, err
	}
	tool.Name = b.Name
	if tool.Version == "" {
		tool.Version = b.Name + " --version"
	}
	tool.Path = "usr/local/bin/" + b.Name
	return s.result(toolchain.AddBinary(spec, b, tool))
}

func (s *Service) result(spec toolchain.Spec) (ToolchainResult, error) {
	dockerfile, err := s.Writer.Write(spec)
	if err != nil {
		return ToolchainResult{}, err
	}
	// Read the spec back as written: its base filled in.
	spec, err = toolchain.Read(dockerfile)
	if err != nil {
		return ToolchainResult{}, err
	}
	return ToolchainResult{
		Toolchain:  spec.Name,
		Path:       "toolchains/" + spec.Name + "/Dockerfile",
		Dockerfile: string(dockerfile),
		Spec:       spec,
	}, nil
}

// ExampleToolchain is the toolchain init_source writes as the example: jq
// from the default package source's default branch.
func (s *Service) ExampleToolchain(ctx context.Context) (toolchain.Spec, error) {
	res, err := s.AddPackages(ctx, Target{Name: "jq"}, []string{"jq"}, nil)
	if err != nil {
		return toolchain.Spec{}, err
	}
	return res.Spec, nil
}

// InitSource renders a new source's files.
func (s *Service) InitSource(ctx context.Context, repository, registry string, renovate bool) ([]source.File, error) {
	example, err := s.ExampleToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return source.Render(s.Writer, source.Options{Repository: repository, Registry: registry, Renovate: renovate, Example: example})
}

func (s *Service) source(name string) (packages.Source, error) {
	if name == "" {
		name = s.DefaultSource
	}
	return s.Sources.Get(name)
}
