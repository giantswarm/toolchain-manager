// Package toolchain writes a toolchain's Dockerfile from the packages and
// binaries a person picked: a build stage that installs them under the
// toolchain's mount directory and writes a wrapper per tool in bin/, then a
// scratch image of that directory with the io.giantswarm.toolchain labels
// the contract check reads. The picks travel in the Dockerfile itself, on
// its spec line, so the manager reads them back to change the toolchain; a
// Dockerfile without it, or one whose text no longer matches its checksum
// line, is hand-written, the advanced mode, and is not edited.
package toolchain

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/giantswarm/toolchain-manager/internal/binaries"
	"github.com/giantswarm/toolchain-manager/internal/packages"
)

// DefaultMountRoot is where a Session mounts its toolchains, one directory
// each: kagent-dev/kagent#3192's /opt/kagent/tools/<name>.
const DefaultMountRoot = "/opt/kagent/tools"

// Spec is what a toolchain is made of.
type Spec struct {
	Name string `json:"name"`
	// Source is the package source of Packages and the build stage's base.
	Source string `json:"source"`
	// Branch is the source's branch the toolchain builds on.
	Branch string `json:"branch"`
	// Base is the build stage's digest-pinned image; empty takes the
	// source's for Branch when the Dockerfile is written.
	Base      string            `json:"base,omitempty"`
	Harnesses []string          `json:"harnesses"`
	Packages  []packages.Pin    `json:"packages,omitempty"`
	Binaries  []binaries.Binary `json:"binaries,omitempty"`
	// Tools are the declared tools, a bin/ wrapper and a version command
	// each. An environment value's {prefix} is the mount directory.
	Tools []packages.Tool `json:"tools"`
}

// Writer writes and reads the Dockerfiles of toolchains.
type Writer struct {
	Sources *packages.Registry
	// MountRoot is the directory the toolchains are mounted under, each at
	// <MountRoot>/<name>; DefaultMountRoot when empty.
	MountRoot string
}

// ErrHandWritten is returned for a Dockerfile the manager did not write, or
// one edited since.
var ErrHandWritten = errors.New("a hand-written Dockerfile: the toolchain-manager does not edit it")

const (
	specMarker     = "# toolchain-manager: "
	checksumMarker = "# toolchain-manager sha256: "
)

var (
	//go:embed scripts/toolchain-wrap
	wrapScript string
	//go:embed scripts/toolchain-fetch
	fetchScript string

	namePattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	toolPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]*$`)
	envPattern        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	pathPattern       = regexp.MustCompile(`^[A-Za-z0-9._+/-]+$`)
	shaPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pinPattern        = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
	urlPattern        = regexp.MustCompile(`^https://[^\s'"\\]+$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	basePattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	harnessPattern    = namePattern
)

// Prefix is the toolchain's mount directory, its build prefix.
func (w Writer) Prefix(name string) string {
	root := w.MountRoot
	if root == "" {
		root = DefaultMountRoot
	}
	return strings.TrimSuffix(root, "/") + "/" + name
}

// Validate checks a spec before it is written.
func (s Spec) Validate() error {
	if !namePattern.MatchString(s.Name) {
		return fmt.Errorf("toolchain name %q: want a lower-case name ([a-z0-9-])", s.Name)
	}
	if !namePattern.MatchString(s.Source) || !pinPattern.MatchString(s.Branch) {
		return fmt.Errorf("toolchain %s: source %q, branch %q", s.Name, s.Source, s.Branch)
	}
	if s.Base != "" && !basePattern.MatchString(s.Base) {
		return fmt.Errorf("toolchain %s: base %q, want a digest-pinned image reference", s.Name, s.Base)
	}
	if len(s.Harnesses) == 0 {
		return fmt.Errorf("toolchain %s declares no Harness", s.Name)
	}
	for _, h := range s.Harnesses {
		if !harnessPattern.MatchString(h) {
			return fmt.Errorf("toolchain %s: Harness %q is no name", s.Name, h)
		}
	}
	if len(s.Packages) == 0 && len(s.Binaries) == 0 {
		return fmt.Errorf("toolchain %s has no package and no binary", s.Name)
	}
	seen := map[string]bool{}
	for _, p := range s.Packages {
		if !pinPattern.MatchString(p.Name) || !pinPattern.MatchString(p.Version) {
			return fmt.Errorf("toolchain %s: package %q at %q is no pin", s.Name, p.Name, p.Version)
		}
		if seen["p:"+p.Name] {
			return fmt.Errorf("toolchain %s: package %s twice", s.Name, p.Name)
		}
		seen["p:"+p.Name] = true
	}
	for _, b := range s.Binaries {
		if !toolPattern.MatchString(b.Name) || !pinPattern.MatchString(b.Version) {
			return fmt.Errorf("toolchain %s: binary %q at %q is no pin", s.Name, b.Name, b.Version)
		}
		if seen["b:"+b.Name] {
			return fmt.Errorf("toolchain %s: binary %s twice", s.Name, b.Name)
		}
		seen["b:"+b.Name] = true
		if b.Path != "" && !pathPattern.MatchString(b.Path) {
			return fmt.Errorf("toolchain %s: binary %s: path %q", s.Name, b.Name, b.Path)
		}
		if !repositoryPattern.MatchString(b.From) && !urlPattern.MatchString(b.From) {
			return fmt.Errorf("toolchain %s: binary %s comes from %q, want owner/name or an https URL", s.Name, b.Name, b.From)
		}
		if len(b.Assets) == 0 {
			return fmt.Errorf("toolchain %s: binary %s has no asset", s.Name, b.Name)
		}
		for _, a := range b.Assets {
			if !slices.Contains(binaries.Arches, a.Arch) {
				return fmt.Errorf("toolchain %s: binary %s: architecture %q", s.Name, b.Name, a.Arch)
			}
			if !urlPattern.MatchString(a.URL) || !shaPattern.MatchString(a.SHA256) {
				return fmt.Errorf("toolchain %s: binary %s %s: want an https URL and a SHA-256 checksum", s.Name, b.Name, a.Arch)
			}
		}
	}
	if len(s.Tools) == 0 {
		return fmt.Errorf("toolchain %s declares no tool", s.Name)
	}
	for _, t := range s.Tools {
		if !toolPattern.MatchString(t.Name) {
			return fmt.Errorf("toolchain %s: tool name %q", s.Name, t.Name)
		}
		if seen["t:"+t.Name] {
			return fmt.Errorf("toolchain %s: tool %s twice", s.Name, t.Name)
		}
		seen["t:"+t.Name] = true
		if strings.TrimSpace(t.Version) == "" {
			return fmt.Errorf("toolchain %s: tool %s has no version command", s.Name, t.Name)
		}
		if strings.ContainsFunc(t.Version, unicode.IsControl) {
			return fmt.Errorf("toolchain %s: tool %s: its version command holds a control character", s.Name, t.Name)
		}
		if t.Path != "" && (!pathPattern.MatchString(t.Path) || strings.HasPrefix(t.Path, "/") || strings.Contains(t.Path, "..")) {
			return fmt.Errorf("toolchain %s: tool %s: path %q, want one relative to the prefix", s.Name, t.Name, t.Path)
		}
		for k, v := range t.Env {
			if !envPattern.MatchString(k) || strings.ContainsAny(v, "'\n") {
				return fmt.Errorf("toolchain %s: tool %s: environment %s", s.Name, t.Name, k)
			}
		}
	}
	return nil
}

// Write returns the Dockerfile of spec.
func (w Writer) Write(spec Spec) ([]byte, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	src, err := w.Sources.Get(spec.Source)
	if err != nil {
		return nil, err
	}
	prefix := w.Prefix(spec.Name)
	install, err := src.Install(spec.Branch, prefix, spec.Packages)
	if err != nil {
		return nil, err
	}
	if spec.Base == "" {
		spec.Base = install.Base
	}
	specLine, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, `# The toolchain %[1]s, written by the toolchain-manager from the packages
# and binaries picked for it: change it with add_packages and add_binary. The
# spec line below is what the manager reads back, the checksum line covers
# the rest of the file: a hand edit makes it a hand-written toolchain, which
# the manager leaves alone.
%[2]s%[3]s

# The build stage installs everything at the mount directory %[4]s,
# since shebangs and library paths embed it, and writes a wrapper per tool
# in bin/.
FROM %[5]s AS build
ARG TARGETARCH
COPY --chmod=0755 <<'TOOLCHAIN_WRAP' /usr/local/bin/toolchain-wrap
%[6]sTOOLCHAIN_WRAP
`, spec.Name, specMarker, specLine, prefix, spec.Base, wrapScript)
	if len(spec.Binaries) > 0 {
		fmt.Fprintf(&b, "COPY --chmod=0755 <<'TOOLCHAIN_FETCH' /usr/local/bin/toolchain-fetch\n%sTOOLCHAIN_FETCH\n", fetchScript)
	}
	if install.Run != "" {
		fmt.Fprintf(&b, "\n# Packages of the %s source on %s, pinned.\nRUN %s\n", src.Name(), spec.Branch, install.Run)
	}
	for _, bin := range spec.Binaries {
		fmt.Fprintf(&b, "\n# %s %s from %s, pinned by its checksum.\n", bin.Name, bin.Version, bin.From)
		b.WriteString("RUN case \"${TARGETARCH}\" in \\\n")
		for _, a := range bin.Assets {
			fmt.Fprintf(&b, "      %s) set -- %s %s ;; \\\n", a.Arch, shq(a.URL), a.SHA256)
		}
		fmt.Fprintf(&b, "      *) echo \"%s has no asset for ${TARGETARCH}\" >&2; exit 1 ;; \\\n", bin.Name)
		fmt.Fprintf(&b, "    esac \\\n && toolchain-fetch %s %s \"$1\" \"$2\" %s\n", prefix, bin.Name, shq(bin.Path))
	}
	b.WriteString("\n# bin/: a wrapper per tool, its environment set.\nRUN ")
	for i, t := range spec.Tools {
		if i > 0 {
			b.WriteString(" \\\n && ")
		}
		fmt.Fprintf(&b, "toolchain-wrap %s %s %s", prefix, t.Name, shq(t.Path))
		for _, k := range sortedKeys(t.Env) {
			fmt.Fprintf(&b, " %s", shq(k+"="+strings.ReplaceAll(t.Env[k], "{prefix}", prefix)))
		}
	}
	b.WriteString("\n")

	labels := []string{
		label("io.giantswarm.toolchain.name", spec.Name),
		label("io.giantswarm.toolchain.harnesses", strings.Join(spec.Harnesses, ",")),
	}
	for _, t := range spec.Tools {
		labels = append(labels, label("io.giantswarm.toolchain.tool."+t.Name, t.Version))
	}
	for _, p := range spec.Packages {
		labels = append(labels, label("io.giantswarm.toolchain.package."+p.Name, p.Version))
	}
	for _, bin := range spec.Binaries {
		labels = append(labels, label("io.giantswarm.toolchain.binary."+bin.Name, bin.Version))
	}
	fmt.Fprintf(&b, `
# The image is the mount directory: no user, no entrypoint.
FROM scratch
COPY --from=build %s/ /
LABEL %s
`, prefix, strings.Join(labels, " \\\n      "))
	text := b.String()
	// The checksum line follows the spec line.
	i := strings.Index(text, specMarker)
	i += strings.IndexByte(text[i:], '\n') + 1
	return []byte(text[:i] + checksumMarker + checksum(text) + "\n" + text[i:]), nil
}

// Read returns the spec of a Dockerfile the manager wrote, ErrHandWritten for
// any other: one without the spec and checksum lines, or one whose text no
// longer matches its checksum.
func Read(dockerfile []byte) (Spec, error) {
	var (
		spec           Spec
		specLine, sum  string
		rest           []string
		foundSpec, got bool
	)
	for _, line := range strings.SplitAfter(string(dockerfile), "\n") {
		if s, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r\n"), checksumMarker); ok && !got {
			sum, got = strings.TrimSpace(s), true
			continue
		}
		if s, ok := strings.CutPrefix(line, specMarker); ok && !foundSpec {
			specLine, foundSpec = strings.TrimSpace(s), true
		}
		rest = append(rest, line)
	}
	if !foundSpec || !got {
		return Spec{}, ErrHandWritten
	}
	if checksum(strings.Join(rest, "")) != sum {
		return Spec{}, fmt.Errorf("%w: edited since the manager wrote it", ErrHandWritten)
	}
	if err := json.Unmarshal([]byte(specLine), &spec); err != nil {
		return Spec{}, fmt.Errorf("%w: its spec line: %v", ErrHandWritten, err)
	}
	return spec, nil
}

// shq quotes s for /bin/sh.
func shq(s string) string {
	if s != "" && pathPattern.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// label is a LABEL key="value" pair, the value escaped for the Dockerfile:
// a $ would be expanded.
func label(k, v string) string {
	return k + `="` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(v) + `"`
}

// checksum is the SHA-256 of a Dockerfile without its checksum line, its
// line endings and trailing newlines normalised: a Dockerfile that travels
// through JSON, a shell or an editor keeps its checksum unless its text
// changed.
func checksum(dockerfile string) string {
	text := strings.TrimRight(strings.ReplaceAll(dockerfile, "\r\n", "\n"), "\n")
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
