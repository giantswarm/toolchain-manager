// Package binaries resolves a single binary a toolchain adds without a
// package source -- a release asset of a tool's GitHub release, or a URL --
// to its version and, per architecture, its download URL and SHA-256
// checksum, which the Dockerfile writer pins.
package binaries

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Arches are the architectures a toolchain image is built for, Docker's
// TARGETARCH names.
var Arches = []string{"amd64", "arm64"}

// machine is each architecture's `uname -m`.
var machine = map[string]string{"amd64": "x86_64", "arm64": "aarch64"}

// Binary is a binary pinned by version and checksum.
type Binary struct {
	// Name is the command it installs, usr/local/bin/<name> under the prefix.
	Name    string `json:"name"`
	Version string `json:"version"`
	// From is where it comes from: the GitHub repository (owner/name) or the
	// URL template.
	From   string  `json:"from"`
	Assets []Asset `json:"assets"`
	// Path is the file within an archive asset; empty: the archive's file
	// called Name.
	Path string `json:"path,omitempty"`
}

// Asset is a binary's download for one architecture.
type Asset struct {
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Request names a binary to resolve: a GitHub release asset (Repository,
// Version, Asset) or a URL (URL, Version).
type Request struct {
	Name string
	// Repository is the GitHub repository, owner/name.
	Repository string
	// Version is the release's tag, or the version a URL template takes.
	Version string
	// Asset is the release asset's name, URL the download's; both are
	// templates of {version} (the version without a leading v), {tag} (the
	// version as given), {arch} (amd64, arm64) and {machine} (x86_64,
	// aarch64).
	Asset string
	URL   string
	// SHA256 are known checksums by architecture; a missing one is computed
	// from the download.
	SHA256 map[string]string
	Path   string
	// Arches to resolve, every one of Arches by default.
	Arches []string
}

// Resolver resolves requests through GitHub's API and plain downloads.
type Resolver struct {
	// API is GitHub's API base URL, https://api.github.com by default.
	API    string
	Token  string
	Client *http.Client
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]*$`)

// Resolve returns the binary req names, every architecture pinned.
func (r Resolver) Resolve(ctx context.Context, req Request) (Binary, error) {
	if !namePattern.MatchString(req.Name) {
		return Binary{}, fmt.Errorf("binary name %q: want a command name ([a-z0-9._+-])", req.Name)
	}
	if req.Version == "" {
		return Binary{}, fmt.Errorf("binary %s: no version", req.Name)
	}
	if (req.Repository == "") == (req.URL == "") {
		return Binary{}, fmt.Errorf("binary %s: name either a GitHub repository with its asset or a URL", req.Name)
	}
	if req.Repository != "" && req.Asset == "" {
		return Binary{}, fmt.Errorf("binary %s: no asset of %s's release %s named", req.Name, req.Repository, req.Version)
	}
	arches := req.Arches
	if len(arches) == 0 {
		arches = Arches
	}
	b := Binary{Name: req.Name, Version: req.Version, Path: req.Path, From: req.Repository}
	var release *release
	if req.Repository != "" {
		rel, err := r.release(ctx, req.Repository, req.Version)
		if err != nil {
			return Binary{}, err
		}
		release = rel
	} else {
		b.From = req.URL
	}
	for _, arch := range arches {
		if _, ok := machine[arch]; !ok {
			return Binary{}, fmt.Errorf("binary %s: architecture %q, want one of %s", req.Name, arch, strings.Join(Arches, ", "))
		}
		a := Asset{Arch: arch, SHA256: strings.ToLower(req.SHA256[arch])}
		if release != nil {
			name := expand(req.Asset, req.Version, arch)
			ra, ok := release.asset(name)
			if !ok {
				return Binary{}, fmt.Errorf("binary %s: release %s of %s has no asset %q", req.Name, req.Version, req.Repository, name)
			}
			a.URL = ra.URL
			if d, ok := strings.CutPrefix(ra.Digest, "sha256:"); ok && a.SHA256 == "" {
				a.SHA256 = d
			}
		} else {
			a.URL = expand(req.URL, req.Version, arch)
		}
		if a.SHA256 == "" {
			sum, err := r.sha256(ctx, a.URL)
			if err != nil {
				return Binary{}, fmt.Errorf("binary %s: %w", req.Name, err)
			}
			a.SHA256 = sum
		}
		if len(a.SHA256) != 64 {
			return Binary{}, fmt.Errorf("binary %s %s: checksum %q is no SHA-256", req.Name, arch, a.SHA256)
		}
		b.Assets = append(b.Assets, a)
	}
	return b, nil
}

func expand(tmpl, version, arch string) string {
	return strings.NewReplacer(
		"{version}", strings.TrimPrefix(version, "v"),
		"{tag}", version,
		"{arch}", arch,
		"{machine}", machine[arch],
	).Replace(tmpl)
}

type release struct {
	Assets []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
}

func (rel *release) asset(name string) (releaseAsset, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return releaseAsset{}, false
}

func (r Resolver) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

func (r Resolver) release(ctx context.Context, repo, tag string) (*release, error) {
	if strings.Count(repo, "/") != 1 {
		return nil, fmt.Errorf("repository %q: want owner/name", repo)
	}
	api := r.API
	if api == "" {
		api = "https://api.github.com"
	}
	u := fmt.Sprintf("%s/repos/%s/releases/tags/%s", strings.TrimSuffix(api, "/"), repo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s has no release %s", repo, tag)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	return &rel, nil
}

func (r Resolver) sha256(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", fmt.Errorf("GET %s: %w", u, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
