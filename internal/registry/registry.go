// Package registry asks an OCI registry, anonymously, whether an image tag
// exists: the published image of a toolchain. A registry that answers
// anonymous pulls (gsoci.azurecr.io) is read through its token flow; one
// that needs credentials is ErrUnknown, as the manager holds none of a
// source's.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrUnknown is a registry that does not answer an anonymous pull.
var ErrUnknown = errors.New("the registry does not answer anonymously")

// manifestTypes are the manifests a toolchain image's tag points at.
var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Client checks image tags.
type Client struct {
	HTTP *http.Client
	// Scheme is https; tests set http.
	Scheme string
}

// Exists reports whether image, host/repository:tag, exists: ErrUnknown
// when the registry wants credentials.
func (c *Client) Exists(ctx context.Context, image string) (bool, error) {
	host, rest, ok := strings.Cut(image, "/")
	repo, tag, okTag := strings.Cut(rest, ":")
	if !ok || !okTag || repo == "" || tag == "" {
		return false, fmt.Errorf("image %q: want host/repository:tag", image)
	}
	scheme := c.Scheme
	if scheme == "" {
		scheme = "https"
	}
	manifest := scheme + "://" + host + "/v2/" + repo + "/manifests/" + url.PathEscape(tag)
	resp, err := c.head(ctx, manifest, "")
	if err != nil {
		return false, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		token, err := c.token(ctx, resp.Header.Get("WWW-Authenticate"))
		if err != nil {
			return false, err
		}
		if resp, err = c.head(ctx, manifest, token); err != nil {
			return false, err
		}
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return false, ErrUnknown
	}
	return false, fmt.Errorf("HEAD %s: %s", manifest, resp.Status)
}

func (c *Client) head(ctx context.Context, u, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestTypes)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()
	return resp, nil
}

// token asks the realm of a Bearer challenge for an anonymous token.
func (c *Client) token(ctx context.Context, challenge string) (string, error) {
	params, ok := bearer(challenge)
	if !ok || params["realm"] == "" {
		return "", ErrUnknown
	}
	q := url.Values{}
	for _, k := range []string{"service", "scope"} {
		if v := params[k]; v != "" {
			q.Set(k, v)
		}
	}
	sep := "?"
	if strings.Contains(params["realm"], "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params["realm"]+sep+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", ErrUnknown
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	if t.Token != "" {
		return t.Token, nil
	}
	if t.AccessToken != "" {
		return t.AccessToken, nil
	}
	return "", ErrUnknown
}

// bearer parses a WWW-Authenticate Bearer challenge's parameters.
func bearer(challenge string) (map[string]string, bool) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(challenge), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return nil, false
	}
	params := map[string]string{}
	for rest != "" {
		key, after, ok := strings.Cut(strings.TrimLeft(rest, " ,"), "=")
		if !ok {
			break
		}
		var value string
		if strings.HasPrefix(after, `"`) {
			end := strings.Index(after[1:], `"`)
			if end < 0 {
				break
			}
			value, rest = after[1:end+1], after[end+2:]
		} else {
			value, rest, _ = strings.Cut(after, ",")
		}
		params[strings.ToLower(strings.TrimSpace(key))] = value
	}
	return params, true
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}
