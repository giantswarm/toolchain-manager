// Package gateway posts into a team's Slack channel through klaus-gateway's
// team-review endpoint: a notice that asks for nothing (POST /notices), and
// an ask with an Approve button (POST /reviews) whose click calls a muster
// tool as the clicking member. The manager authenticates with its own
// projected ServiceAccount token for the gateway's audience, which the
// gateway verifies through the TokenReview API and its allow-list.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client posts to klaus-gateway.
type Client struct {
	// URL is klaus-gateway's base URL, http(s)://host[:port].
	URL string
	// TokenFile holds the projected ServiceAccount token, read on every
	// post since the kubelet rotates it.
	TokenFile string
	HTTP      *http.Client
}

// Tool is the muster tool a click calls, verbatim, as the member.
type Tool struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Review is an ask to a team: the change spelled out, its pull requests and
// the tool its Approve calls.
type Review struct {
	Team         string   `json:"team"`
	Channel      string   `json:"channel"`
	Text         string   `json:"text"`
	Actor        string   `json:"actor,omitempty"`
	PullRequests []string `json:"pullRequests,omitempty"`
	Link         string   `json:"link,omitempty"`
	Approve      Tool     `json:"approve"`
}

// Notice is a message that asks for nothing.
type Notice struct {
	Team         string   `json:"team"`
	Channel      string   `json:"channel"`
	Text         string   `json:"text"`
	Link         string   `json:"link,omitempty"`
	PullRequests []string `json:"pullRequests,omitempty"`
}

// Posted locates a posted message; ID is a review's handle.
type Posted struct {
	ID      string `json:"id,omitempty"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// textMax is the gateway's bound on a text.
const textMax = 3000

// Ask posts a review.
func (c *Client) Ask(ctx context.Context, r Review) (Posted, error) {
	r.Text = clip(r.Text)
	return c.post(ctx, "/reviews", r)
}

// Notice posts a notice into a team's channel.
func (c *Client) Notice(ctx context.Context, team, channel, text, link string) error {
	_, err := c.post(ctx, "/notices", Notice{Team: team, Channel: channel, Text: clip(text), Link: link})
	return err
}

func (c *Client) post(ctx context.Context, path string, in any) (Posted, error) {
	token, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return Posted{}, fmt.Errorf("read the ServiceAccount token for klaus-gateway: %w", err)
	}
	body, err := json.Marshal(in)
	if err != nil {
		return Posted{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return Posted{}, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Posted{}, fmt.Errorf("klaus-gateway POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Posted{}, fmt.Errorf("klaus-gateway POST %s: %s: %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	var out Posted
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil && !errors.Is(err, io.EOF) {
		return Posted{}, fmt.Errorf("klaus-gateway POST %s: %w", path, err)
	}
	return out, nil
}

func clip(s string) string {
	if r := []rune(s); len(r) > textMax {
		return string(r[:textMax-1]) + "…"
	}
	return s
}
