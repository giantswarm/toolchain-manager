package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Branch is the commit a branch points at: ErrNotFound when there is no
// such branch.
func (c *Client) Branch(ctx context.Context, token, repository, branch string) (string, error) {
	path, err := repoPath(repository)
	if err != nil {
		return "", err
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err = c.get(ctx, token, path+"/git/ref/heads/"+escapePath(branch), &ref)
	return ref.Object.SHA, err
}

// SetBranch points branch at sha, creating it when create is set, else
// moving it there.
func (c *Client) SetBranch(ctx context.Context, token, repository, branch, sha string, create bool) error {
	path, err := repoPath(repository)
	if err != nil {
		return err
	}
	if create {
		return c.send(ctx, token, http.MethodPost, path+"/git/refs", map[string]string{"ref": "refs/heads/" + branch, "sha": sha}, nil)
	}
	return c.send(ctx, token, http.MethodPatch, path+"/git/refs/heads/"+escapePath(branch), map[string]any{"sha": sha, "force": true}, nil)
}

// PutFile commits content as file on branch with message, as the person:
// a new file, or with sha (the blob it replaces, FileSHA) an existing one.
func (c *Client) PutFile(ctx context.Context, token, repository, branch, file, message, sha string, content []byte) error {
	path, err := repoPath(repository)
	if err != nil {
		return err
	}
	in := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
		"branch":  branch,
	}
	if sha != "" {
		in["sha"] = sha
	}
	return c.send(ctx, token, http.MethodPut, path+"/contents/"+escapePath(file), in, nil)
}

// FileSHA is the blob SHA of file at ref: ErrNotFound when there is none.
func (c *Client) FileSHA(ctx context.Context, token, repository, file, ref string) (string, error) {
	path, err := repoPath(repository)
	if err != nil {
		return "", err
	}
	var f struct {
		SHA string `json:"sha"`
	}
	err = c.get(ctx, token, path+"/contents/"+escapePath(file)+"?ref="+url.QueryEscape(ref), &f)
	return f.SHA, err
}

// PullRequest is a pull request as the person's token reads it.
type PullRequest struct {
	Number int `json:"number"`
	// NodeID is the pull request's GraphQL ID, which auto-merge is armed on.
	NodeID         string     `json:"node_id"`
	HTMLURL        string     `json:"html_url"`
	Title          string     `json:"title"`
	State          string     `json:"state"`
	MergedAt       *time.Time `json:"merged_at"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

// Merged reports whether the pull request was merged.
func (p PullRequest) Merged() bool { return p.MergedAt != nil }

// CreatePull opens a pull request from head into base, as the person.
func (c *Client) CreatePull(ctx context.Context, token, repository, head, base, title, body string) (PullRequest, error) {
	path, err := repoPath(repository)
	if err != nil {
		return PullRequest{}, err
	}
	var pr PullRequest
	err = c.send(ctx, token, http.MethodPost, path+"/pulls", map[string]string{"head": head, "base": base, "title": title, "body": body}, &pr)
	return pr, err
}

// Pull reads pull request number.
func (c *Client) Pull(ctx context.Context, token, repository string, number int) (PullRequest, error) {
	path, err := repoPath(repository)
	if err != nil {
		return PullRequest{}, err
	}
	var pr PullRequest
	err = c.get(ctx, token, fmt.Sprintf("%s/pulls/%d", path, number), &pr)
	return pr, err
}

// PullsFrom lists the pull requests from branch of the repository itself,
// open and closed, newest first.
func (c *Client) PullsFrom(ctx context.Context, token, repository, branch string) ([]PullRequest, error) {
	path, err := repoPath(repository)
	if err != nil {
		return nil, err
	}
	owner, _, _ := strings.Cut(repository, "/")
	var prs []PullRequest
	err = c.get(ctx, token, path+"/pulls?state=all&sort=created&direction=desc&per_page=10&head="+url.QueryEscape(owner+":"+branch), &prs)
	return prs, err
}

// Job states of a commit's CI.
const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobSuccess = "success"
	JobFailed  = "failed"
	JobSkipped = "skipped"
)

// Job is one CI result on a commit: a check run (a GitHub Actions job) or a
// commit status (CircleCI), with the page of its log.
type Job struct {
	Name  string `json:"name"`
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

// Jobs reads a commit's check runs and commit statuses.
func (c *Client) Jobs(ctx context.Context, token, repository, sha string) ([]Job, error) {
	path, err := repoPath(repository)
	if err != nil {
		return nil, err
	}
	var runs struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	if err := c.get(ctx, token, path+"/commits/"+url.PathEscape(sha)+"/check-runs?per_page=100", &runs); err != nil {
		return nil, err
	}
	var combined struct {
		Statuses []struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := c.get(ctx, token, path+"/commits/"+url.PathEscape(sha)+"/status?per_page=100", &combined); err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(runs.CheckRuns)+len(combined.Statuses))
	for _, r := range runs.CheckRuns {
		jobs = append(jobs, Job{Name: r.Name, State: checkRunState(r.Status, r.Conclusion), URL: r.HTMLURL})
	}
	for _, s := range combined.Statuses {
		state := JobRunning
		switch s.State {
		case "success":
			state = JobSuccess
		case "failure", "error":
			state = JobFailed
		}
		jobs = append(jobs, Job{Name: s.Context, State: state, URL: s.TargetURL})
	}
	return jobs, nil
}

func checkRunState(status, conclusion string) string {
	switch status {
	case "in_progress":
		return JobRunning
	case "completed":
	default:
		return JobQueued
	}
	switch conclusion {
	case "success":
		return JobSuccess
	case "skipped", "neutral":
		return JobSkipped
	}
	return JobFailed
}

// TagsAt lists the tags with prefix that point at commit sha, annotated
// tags followed to their commit.
func (c *Client) TagsAt(ctx context.Context, token, repository, prefix, sha string) ([]string, error) {
	path, err := repoPath(repository)
	if err != nil {
		return nil, err
	}
	type object struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	var refs []struct {
		Ref    string `json:"ref"`
		Object object `json:"object"`
	}
	if err := c.get(ctx, token, path+"/git/matching-refs/tags/"+escapePath(prefix), &refs); err != nil {
		return nil, err
	}
	var tags []string
	for _, r := range refs {
		target := r.Object
		if target.Type == "tag" {
			var tag struct {
				Object object `json:"object"`
			}
			if err := c.get(ctx, token, path+"/git/tags/"+url.PathEscape(target.SHA), &tag); err != nil {
				return nil, err
			}
			target = tag.Object
		}
		if target.SHA == sha {
			tags = append(tags, strings.TrimPrefix(r.Ref, "refs/tags/"))
		}
	}
	return tags, nil
}

// ReleaseByTag reads the GitHub Release of tag: ErrNotFound when there is
// none.
func (c *Client) ReleaseByTag(ctx context.Context, token, repository, tag string) (Release, error) {
	path, err := repoPath(repository)
	if err != nil {
		return Release{}, err
	}
	var r Release
	err = c.get(ctx, token, path+"/releases/tags/"+escapePath(tag), &r)
	return r, err
}
