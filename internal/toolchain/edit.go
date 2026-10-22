package toolchain

import (
	"github.com/giantswarm/toolchain-manager/internal/binaries"
	"github.com/giantswarm/toolchain-manager/internal/packages"
)

// AddPackages returns spec with pins added, a package it has already
// re-pinned, and tools declared, a tool of the same name replaced.
func AddPackages(spec Spec, pins []packages.Pin, tools []packages.Tool) Spec {
	out := clone(spec)
	for _, p := range pins {
		replaced := false
		for i := range out.Packages {
			if out.Packages[i].Name == p.Name {
				out.Packages[i], replaced = p, true
			}
		}
		if !replaced {
			out.Packages = append(out.Packages, p)
		}
	}
	out.Tools = withTools(out.Tools, tools)
	return out
}

// AddBinary returns spec with b added, a binary of the same name replaced,
// and tool declared.
func AddBinary(spec Spec, b binaries.Binary, tool packages.Tool) Spec {
	out := clone(spec)
	replaced := false
	for i := range out.Binaries {
		if out.Binaries[i].Name == b.Name {
			out.Binaries[i], replaced = b, true
		}
	}
	if !replaced {
		out.Binaries = append(out.Binaries, b)
	}
	out.Tools = withTools(out.Tools, []packages.Tool{tool})
	return out
}

func withTools(have, add []packages.Tool) []packages.Tool {
	for _, t := range add {
		replaced := false
		for i := range have {
			if have[i].Name == t.Name {
				have[i], replaced = t, true
			}
		}
		if !replaced {
			have = append(have, t)
		}
	}
	return have
}

func clone(s Spec) Spec {
	s.Harnesses = append([]string(nil), s.Harnesses...)
	s.Packages = append([]packages.Pin(nil), s.Packages...)
	s.Binaries = append([]binaries.Binary(nil), s.Binaries...)
	s.Tools = append([]packages.Tool(nil), s.Tools...)
	return s
}
