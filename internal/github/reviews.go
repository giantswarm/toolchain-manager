package github

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// EnableAutoMerge arms auto-merge on the pull request (its node ID), as the
// person: GitHub squash-merges it once its required checks pass and its
// required reviews are given. It fails where the repository does not allow
// auto-merge, or the pull request can be merged at once (no required check
// is pending).
func (c *Client) EnableAutoMerge(ctx context.Context, token, nodeID string) error {
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	err := c.send(ctx, token, http.MethodPost, "/graphql", map[string]any{
		"query":     `mutation($id: ID!) { enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: SQUASH}) { clientMutationId } }`,
		"variables": map[string]string{"id": nodeID},
	}, &out)
	if err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		msgs := make([]string, len(out.Errors))
		for i, e := range out.Errors {
			msgs[i] = e.Message
		}
		return errors.New(strings.Join(msgs, "; "))
	}
	return nil
}

// LatestRelease reads owner/name's latest release, the newest that is no
// draft or pre-release: ErrNotFound when there is none. An empty token asks
// anonymously, which reaches a public repository.
func (c *Client) LatestRelease(ctx context.Context, token, repository string) (Release, error) {
	path, err := repoPath(repository)
	if err != nil {
		return Release{}, err
	}
	var r Release
	err = c.get(ctx, token, path+"/releases/latest", &r)
	return r, err
}
