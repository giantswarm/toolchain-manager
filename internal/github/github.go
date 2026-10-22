// Package github asks GitHub, with the person's user-to-server token of the
// toolchain-manager's GitHub App, who the person is and whether the App's
// installation grants a repository. A user-to-server token reaches a
// repository only where the App is installed and the person has access, so a
// repository it reads is one the App's installation grants.
package github

import (
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
	owner, name, ok := strings.Cut(repository, "/")
	if !ok {
		return Repository{}, fmt.Errorf("repository %q: want owner/name", repository)
	}
	var r Repository
	err := c.get(ctx, token, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), &r)
	return r, err
}

func (c *Client) get(ctx context.Context, token, path string, out any) error {
	api := c.API
	if api == "" {
		api = DefaultAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(api, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return json.NewDecoder(resp.Body).Decode(out)
	case http.StatusUnauthorized:
		return ErrRefused
	case http.StatusNotFound:
		return ErrNotFound
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
}
