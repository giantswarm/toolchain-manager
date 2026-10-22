// Package packages is the package source interface of the toolchain-manager:
// a distribution's packages, searched with their versions per branch and
// resolved to their dependency closure, and the install step the Dockerfile
// writer puts into a toolchain's build stage. Alpine's is the first source;
// another distribution plugs in behind Source and is registered by name, with
// no change to the tools that use it.
package packages

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Source is one package source, such as Alpine's.
type Source interface {
	// Name is the source's name, the `source` argument of the tools.
	Name() string
	// Branches are the branches the source searches, newest first. The first
	// is the default branch a toolchain builds on.
	Branches() []string
	// Harnesses are the Harnesses a toolchain built from this source fits:
	// its io.giantswarm.toolchain.harnesses default.
	Harnesses() []string
	// Search returns the packages whose name contains query, each with its
	// version on every branch that carries it.
	Search(ctx context.Context, query string) ([]Package, error)
	// Closure resolves names on branch to the packages an install adds,
	// names included, and the shared libraries they need.
	Closure(ctx context.Context, branch string, names []string) (Closure, error)
	// Tools are the default tools of a picked package: the commands a
	// toolchain made from it declares and wraps in bin/.
	Tools(ctx context.Context, branch, name string) ([]Tool, error)
	// Install is the build-stage step that installs pins of branch under
	// prefix; without pins it names only the build stage's base.
	Install(branch, prefix string, pins []Pin) (Install, error)
}

// Package is a package with its versions per branch.
type Package struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	URL         string            `json:"url,omitempty"`
	License     string            `json:"license,omitempty"`
	Repository  string            `json:"repository,omitempty"`
	Versions    map[string]string `json:"versions"`
}

// Closure is what installing a set of packages on a branch adds.
type Closure struct {
	Branch string `json:"branch"`
	// Packages are every package the install adds, the requested ones
	// included, sorted by name.
	Packages []ClosurePackage `json:"packages"`
	// Libraries are the shared libraries (so:) the closure needs, each with
	// the package that provides it.
	Libraries []Library `json:"libraries"`
	// Unresolved are dependencies no package of the branch provides.
	Unresolved []string `json:"unresolved,omitempty"`
}

// ClosurePackage is one package of a closure.
type ClosurePackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Requested is true for a package the caller named.
	Requested bool `json:"requested,omitempty"`
	// RequiredBy names the packages that pulled it in.
	RequiredBy []string `json:"requiredBy,omitempty"`
	// Size is the installed size in bytes, when the source knows it.
	Size int64 `json:"installedSize,omitempty"`
}

// Library is a shared library of a closure.
type Library struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

// Pin is a package pinned to a version.
type Pin struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (p Pin) String() string { return p.Name + "=" + p.Version }

// Tool is a command a toolchain declares: its bin/ wrapper and its version
// command, the io.giantswarm.toolchain.tool.<name> label.
type Tool struct {
	Name string `json:"name"`
	// Path is the command's file relative to the prefix; empty finds the
	// command in the prefix's usr/local/bin, usr/bin, bin, usr/sbin or sbin.
	Path string `json:"path,omitempty"`
	// Version is the command that prints its version, run by /bin/sh -c.
	Version string `json:"version"`
	// Env is the environment the wrapper sets before it runs the command.
	Env map[string]string `json:"env,omitempty"`
}

// Install is a source's build-stage step.
type Install struct {
	// Base is the digest-pinned image of the build stage.
	Base string
	// Run is the shell command of its RUN instruction.
	Run string
}

// Registry holds the package sources by name.
type Registry struct {
	sources map[string]Source
}

// NewRegistry returns a registry of sources; a name used twice is an error.
func NewRegistry(sources ...Source) (*Registry, error) {
	r := &Registry{sources: map[string]Source{}}
	for _, s := range sources {
		if _, ok := r.sources[s.Name()]; ok {
			return nil, fmt.Errorf("package source %q registered twice", s.Name())
		}
		r.sources[s.Name()] = s
	}
	return r, nil
}

// Get returns the source called name.
func (r *Registry) Get(name string) (Source, error) {
	s, ok := r.sources[name]
	if !ok {
		return nil, fmt.Errorf("no package source %q; the sources are %s", name, strings.Join(r.Names(), ", "))
	}
	return s, nil
}

// Names are the registered sources' names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.sources))
	for n := range r.sources {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
