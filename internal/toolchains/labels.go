package toolchains

import (
	"sort"
	"strings"
)

// Label keys a toolchain's image declares itself with, the ones the
// toolchain contract check reads.
const (
	labelPrefix    = "io.giantswarm.toolchain."
	labelHarnesses = labelPrefix + "harnesses"
	labelEgress    = labelPrefix + "egress"
	labelTool      = labelPrefix + "tool."
	labelPackage   = labelPrefix + "package."
	labelBinary    = labelPrefix + "binary."
)

// Tool is a declared tool: its command and the version command the
// contract check runs.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Declaration is what a toolchain's image declares in its labels, beside
// its name (io.giantswarm.toolchain.name), which the contract check holds
// to the toolchain's directory.
type Declaration struct {
	Harnesses []string `json:"harnesses"`
	Tools     []Tool   `json:"tools"`
	// Packages and Binaries are the pinned versions, by name, of a
	// Dockerfile the manager wrote; a hand-written one may declare none.
	Packages map[string]string `json:"packages,omitempty"`
	Binaries map[string]string `json:"binaries,omitempty"`
	// Egress are the endpoints the tools reach.
	Egress []string `json:"egress,omitempty"`
}

// Declare reads the io.giantswarm.toolchain labels of a Dockerfile's final
// stage: the image's labels, the same for a Dockerfile the manager wrote
// and a hand-written one.
func Declare(dockerfile []byte) Declaration {
	labels := finalLabels(string(dockerfile))
	d := Declaration{Harnesses: list(labels[labelHarnesses])}
	d.Egress = list(labels[labelEgress])
	for k, v := range labels {
		switch {
		case strings.HasPrefix(k, labelTool):
			d.Tools = append(d.Tools, Tool{Name: strings.TrimPrefix(k, labelTool), Version: v})
		case strings.HasPrefix(k, labelPackage):
			if d.Packages == nil {
				d.Packages = map[string]string{}
			}
			d.Packages[strings.TrimPrefix(k, labelPackage)] = v
		case strings.HasPrefix(k, labelBinary):
			if d.Binaries == nil {
				d.Binaries = map[string]string{}
			}
			d.Binaries[strings.TrimPrefix(k, labelBinary)] = v
		}
	}
	sort.Slice(d.Tools, func(i, j int) bool { return d.Tools[i].Name < d.Tools[j].Name })
	return d
}

func list(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// finalLabels returns the key=value labels of the Dockerfile's last stage,
// its instructions joined across line continuations and its comments
// dropped, as the builder reads them.
func finalLabels(dockerfile string) map[string]string {
	labels := map[string]string{}
	var logical strings.Builder
	flush := func() {
		line := strings.TrimSpace(logical.String())
		logical.Reset()
		word, rest, _ := strings.Cut(line, " ")
		switch strings.ToUpper(word) {
		case "FROM":
			labels = map[string]string{}
		case "LABEL":
			for k, v := range pairs(rest) {
				labels[k] = v
			}
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(dockerfile, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if body, ok := strings.CutSuffix(strings.TrimRight(line, " \t"), `\`); ok {
			logical.WriteString(body + " ")
			continue
		}
		logical.WriteString(line)
		flush()
	}
	flush()
	return labels
}

// pairs parses LABEL's key=value pairs, a value quoted with its escapes or
// bare to the next blank.
func pairs(s string) map[string]string {
	out := map[string]string{}
	for {
		s = strings.TrimLeft(s, " \t")
		key, rest, ok := strings.Cut(s, "=")
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return out
		}
		var value strings.Builder
		i := 0
		if strings.HasPrefix(rest, `"`) {
			for i = 1; i < len(rest) && rest[i] != '"'; i++ {
				if rest[i] == '\\' && i+1 < len(rest) {
					i++
				}
				value.WriteByte(rest[i])
			}
			i++
		} else {
			for ; i < len(rest) && rest[i] != ' ' && rest[i] != '\t'; i++ {
				value.WriteByte(rest[i])
			}
		}
		out[strings.Trim(key, `"`)] = value.String()
		if i >= len(rest) {
			return out
		}
		s = rest[i:]
	}
}
