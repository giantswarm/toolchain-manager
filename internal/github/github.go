// Package github asks GitHub, with the person's user-to-server token of the
// toolchain-manager's GitHub App, who the person is, whether the App's
// installation grants a repository, and what a granted repository holds:
// its releases, files, pull requests and the CI results of its commits; and
// it opens a pull request as the person. A user-to-server token reaches a
// repository only where the App is installed and the person has access, so a
// repository it reads or writes is one the App's installation grants.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultAPI is github.com's API.
const DefaultAPI = "https://api.github.com"

var (
	// ErrRefused is GitHub refusing the token (401): revoked or expired.
	ErrRefused = errors.New("GitHub refused the token")
	// ErrNotFound is a repository the token does not reach: the App is not
	// installed on it, or the person has no access.
	ErrNotFound = errors.New("not found")
)

// Client is a GitHub API client authenticating with the token each call
// passes.
type Client struct {
	// API is the API's base URL; empty is DefaultAPI.
	API  string
	HTTP *http.Client
}

// Permissions are the person's permissions on a repository.
type Permissions struct {
	Admin    bool `json:"admin"`
	Maintain bool `json:"maintain"`
	Push     bool `json:"push"`
}

// Repository is a repository as the person's token reads it.
type Repository struct {
	FullName      string      `json:"full_name"`
	HTMLURL       string      `json:"html_url"`
	DefaultBranch string      `json:"default_branch"`
	Private       bool        `json:"private"`
	Permissions   Permissions `json:"permissions"`
}

// Writable reports whether the person may push to the repository.
func (r Repository) Writable() bool {
	return r.Permissions.Admin || r.Permissions.Maintain || r.Permissions.Push
}

// Login is the account the token is the person's on (GET /user).
func (c *Client) Login(ctx context.Context, token string) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := c.get(ctx, token, "/user", &user); err != nil {
		return "", err
	}
	if user.Login == "" {
		return "", errors.New("GET /user: no login in the answer")
	}
	return user.Login, nil
}

// Repository reads owner/name with the person's token: ErrNotFound when the
// App's installation does not grant it or the person cannot read it.
func (c *Client) Repository(ctx context.Context, token, repository string) (Repository, error) {
	path, err := repoPath(repository)
	if err != nil {
		return Repository{}, err
	}
	var r Repository
	err = c.get(ctx, token, path, &r)
	return r, err
}

// Release is a GitHub Release as the person's token reads it.
type Release struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

// maxReleasePages bounds Releases: 1,000 releases.
const maxReleasePages = 10

// Releases lists owner/name's releases, newest first, with the person's
// token: ErrNotFound when the App's installation does not grant it.
func (c *Client) Releases(ctx context.Context, token, repository string) ([]Release, error) {
	path, err := repoPath(repository)
	if err != nil {
		return nil, err
	}
	var all []Release
	for page := 1; page <= maxReleasePages; page++ {
		var batch []Release
		if err := c.get(ctx, token, fmt.Sprintf("%s/releases?per_page=100&page=%d", path, page), &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}

// File reads a file of owner/name at ref (a branch, tag or commit) with the
// person's token: ErrNotFound when there is none, or the App's installation
// does not grant the repository.
func (c *Client) File(ctx context.Context, token, repository, file, ref string) ([]byte, error) {
	path, err := repoPath(repository)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, token, http.MethodGet, path+"/contents/"+escapePath(file)+"?ref="+url.QueryEscape(ref), "application/vnd.github.raw+json", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// escapePath escapes each segment of a slash-separated path.
func escapePath(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

func repoPath(repository string) (string, error) {
	owner, name, ok := strings.Cut(repository, "/")
	if !ok {
		return "", fmt.Errorf("repository %q: want owner/name", repository)
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func (c *Client) get(ctx context.Context, token, path string, out any) error {
	return c.send(ctx, token, http.MethodGet, path, nil, out)
}

// send sends method path with in as its JSON body and decodes a success's
// answer into out, when out is set.
func (c *Client) send(ctx context.Context, token, method, path string, in, out any) error {
	resp, err := c.do(ctx, token, method, path, "application/vnd.github+json", in)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// do sends method path, with in as its JSON body when set, and returns a
// success's response, its body for the caller to close.
func (c *Client) do(ctx context.Context, token, method, path, accept string, in any) (*http.Response, error) {
	api := c.API
	if api == "" {
		api = DefaultAPI
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(api, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, ErrRefused
	case http.StatusNotFound:
		return nil, ErrNotFound
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
}
