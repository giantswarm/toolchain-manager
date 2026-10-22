// Package source renders the toolchain source template init_source writes
// into a new repository: its layout, the CI that runs the toolchain contract
// check on every pull request and publishes each changed toolchain on a
// merge, the optional Renovate configuration and one example toolchain whose
// Dockerfile the manager wrote.
package source

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/giantswarm/toolchain-manager/internal/toolchain"
)

//go:embed all:template
var templateFS embed.FS

// ContractRef is the giantswarm/agent-toolchains commit whose contract check
// and Harness list a source's CI runs.
const ContractRef = "ca7f96683cc249789175b663748d4ec48e6c14f9"

// Options configure a new source.
type Options struct {
	// Repository is the source's repository, owner/name.
	Repository string
	// Registry is the image repository prefix its toolchains publish under:
	// <registry>/<name>.
	Registry string
	// Renovate writes renovate.json5.
	Renovate bool
	// Example is the example toolchain's spec.
	Example toolchain.Spec
}

// File is one file of the rendered source.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	registryPattern   = regexp.MustCompile(`^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)*$`)
)

// Render returns the files of a new source, sorted by path.
func Render(w toolchain.Writer, o Options) ([]File, error) {
	if !repositoryPattern.MatchString(o.Repository) {
		return nil, fmt.Errorf("repository %q: want owner/name", o.Repository)
	}
	if !registryPattern.MatchString(o.Registry) || !strings.Contains(o.Registry, ".") {
		return nil, fmt.Errorf("registry %q: want a registry host and repository prefix, such as registry.example.com/team/toolchains", o.Registry)
	}
	data := map[string]any{
		"Repository":   o.Repository,
		"Registry":     o.Registry,
		"RegistryHost": strings.SplitN(o.Registry, "/", 2)[0],
		"Renovate":     o.Renovate,
		"ContractRef":  ContractRef,
		"MountRoot":    strings.TrimSuffix(w.Prefix(""), "/"),
	}
	var files []File
	err := fs.WalkDir(templateFS, "template", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, "template/")
		if rel == "renovate.json5" && !o.Renovate {
			return nil
		}
		raw, err := templateFS.ReadFile(p)
		if err != nil {
			return err
		}
		t, err := template.New(rel).Delims("[[", "]]").Option("missingkey=error").Parse(string(raw))
		if err != nil {
			return err
		}
		var out bytes.Buffer
		if err := t.Execute(&out, data); err != nil {
			return err
		}
		files = append(files, File{Path: rel, Content: out.String()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	dockerfile, err := w.Write(o.Example)
	if err != nil {
		return nil, fmt.Errorf("the example toolchain: %w", err)
	}
	files = append(files, File{Path: "toolchains/" + o.Example.Name + "/Dockerfile", Content: string(dockerfile)})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}
