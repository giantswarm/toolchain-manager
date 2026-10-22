package toolchain

import (
	"strings"
	"testing"

	"github.com/giantswarm/toolchain-manager/internal/binaries"
	"github.com/giantswarm/toolchain-manager/internal/packages"
)

func valid() Spec {
	return Spec{
		Name: "jq", Source: "alpine", Branch: "v3.22", Harnesses: []string{"claude"},
		Base:     "docker.io/library/alpine:3.22.7@sha256:" + strings.Repeat("a", 64),
		Packages: []packages.Pin{{Name: "jq", Version: "1.8.2-r0"}},
		Tools:    []packages.Tool{{Name: "jq", Version: "jq --version"}},
	}
}

// A spec line is the caller's input: nothing in it may break out of the
// Dockerfile's structure.
func TestValidateRefusesWhatWouldBreakTheDockerfile(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("the valid spec: %v", err)
	}
	binary := binaries.Binary{
		Name: "lint", Version: "v1", From: "owner/repo",
		Assets: []binaries.Asset{{Arch: "amd64", URL: "https://example.com/lint", SHA256: strings.Repeat("b", 64)}},
	}
	for name, edit := range map[string]func(*Spec){
		"an unpinned base":          func(s *Spec) { s.Base = "alpine:3.22" },
		"a base with a newline":     func(s *Spec) { s.Base = "x@sha256:" + strings.Repeat("a", 64) + "\nRUN evil" },
		"a branch with a space":     func(s *Spec) { s.Branch = "v3.22 x" },
		"a version command newline": func(s *Spec) { s.Tools[0].Version = "jq --version\nRUN evil" },
		"an environment quote":      func(s *Spec) { s.Tools[0].Env = map[string]string{"X": "a'b"} },
		"a tool path outside":       func(s *Spec) { s.Tools[0].Path = "../../etc/passwd" },
		"a binary's unknown arch": func(s *Spec) {
			b := binary
			b.Assets = []binaries.Asset{{Arch: "x) evil ;;", URL: "https://example.com/lint", SHA256: strings.Repeat("b", 64)}}
			s.Binaries = []binaries.Binary{b}
		},
		"a binary from a comment line": func(s *Spec) {
			b := binary
			b.From = "owner/repo\nRUN evil"
			s.Binaries = []binaries.Binary{b}
		},
		"a binary's plain-HTTP URL": func(s *Spec) {
			b := binary
			b.Assets = []binaries.Asset{{Arch: "amd64", URL: "http://example.com/lint", SHA256: strings.Repeat("b", 64)}}
			s.Binaries = []binaries.Binary{b}
		},
	} {
		s := valid()
		s.Tools = append([]packages.Tool(nil), s.Tools...)
		edit(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: valid", name)
		}
	}
}

func TestReadRefusesAnEditedOrForeignDockerfile(t *testing.T) {
	for name, dockerfile := range map[string]string{
		"no spec line":      "FROM scratch\n",
		"a broken checksum": specMarker + `{"name":"jq"}` + "\n" + checksumMarker + strings.Repeat("0", 64) + "\nFROM scratch\n",
	} {
		if _, err := Read([]byte(dockerfile)); err == nil {
			t.Errorf("%s: read", name)
		}
	}
}
