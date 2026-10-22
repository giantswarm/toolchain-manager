package toolchains

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/identity"
	"github.com/giantswarm/toolchain-manager/internal/sources"
)

// Build states of a toolchain's pull request, from its opening to its
// published image.
const (
	// StateQueued: no CI job has started on the pull request or, once
	// merged, on its merge commit.
	StateQueued = "queued"
	// StateChecking: the pull request's checks, the contract check among
	// them, run.
	StateChecking = "checking"
	// StateChecked: every check passed; the merge publishes.
	StateChecked = "checked"
	// StateBuilding: merged, the release builds, checks and publishes.
	StateBuilding = "building"
	// StatePublished: the merge commit is released as <name>/vX.Y.Z.
	StatePublished = "published"
	// StateFailed: a job failed; Log is its log.
	StateFailed = "failed"
	// StateUnreleased: merged and every job passed without a release of the
	// toolchain at the merge commit.
	StateUnreleased = "unreleased"
	// StateClosed: the pull request was closed without a merge.
	StateClosed = "closed"
)

// Wait bounds of toolchain_build_status: it reads every statusPoll and
// answers within statusMaxWait, inside muster's default call timeout.
var (
	statusPoll    = 5 * time.Second
	statusMaxWait = 25 * time.Second
)

// Build is a toolchain's build state.
type Build struct {
	Toolchain  string `json:"toolchain"`
	Source     string `json:"source"`
	Repository string `json:"repository"`
	State      string `json:"state"`
	Pull       int    `json:"pull,omitempty"`
	PullURL    string `json:"pullURL,omitempty"`
	// Commit is the commit the state is of: the pull request's head, or its
	// merge commit.
	Commit string `json:"commit,omitempty"`
	// Log is the failed job's log.
	Log string `json:"log,omitempty"`
	// Jobs are the commit's CI jobs, skipped ones left out.
	Jobs []github.Job `json:"jobs,omitempty"`
	// Version, Image and ReleaseURL are the published version's.
	Version    string `json:"version,omitempty"`
	Image      string `json:"image,omitempty"`
	ReleaseURL string `json:"releaseURL,omitempty"`
}

// StatusResult is toolchain_build_status' answer.
type StatusResult struct {
	Build *Build `json:"build,omitempty"`
	Answer
}

// BuildStatus reads the build state of toolchain name's pull request in
// the offered source ref names: pull, or the newest from the branch
// create_toolchain opens. With wait it reads again until the state moves
// or statusMaxWait passes.
func (c *Catalog) BuildStatus(ctx context.Context, ref, name string, pull int, wait bool) (StatusResult, error) {
	name = strings.TrimSpace(name)
	if !namePattern.MatchString(name) {
		return StatusResult{}, errors.New("name the toolchain, as create_toolchain opened it")
	}
	src, err := c.one(ctx, ref)
	if err != nil {
		return StatusResult{}, err
	}
	token, ok := identity.Grant(ctx)
	if !ok {
		return StatusResult{Answer: c.grantRequired()}, nil
	}
	b, err := c.build(ctx, token, src, name, pull)
	if wait && err == nil {
		deadline := time.Now().Add(statusMaxWait)
		for first := b.State; b.State == first && time.Now().Add(statusPoll).Before(deadline); {
			select {
			case <-ctx.Done():
				return StatusResult{}, ctx.Err()
			case <-time.After(statusPoll):
			}
			if b, err = c.build(ctx, token, src, name, pull); err != nil {
				break
			}
		}
	}
	if errors.Is(err, github.ErrRefused) {
		return StatusResult{Answer: c.grantRequired()}, nil
	}
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{Build: b, Answer: Answer{Message: b.message()}}, nil
}

func (c *Catalog) build(ctx context.Context, token string, src sources.Source, name string, number int) (*Build, error) {
	b := &Build{Toolchain: name, Source: src.Name, Repository: src.Repository}
	var pr github.PullRequest
	var err error
	if number > 0 {
		pr, err = c.GitHub.Pull(ctx, token, src.Repository, number)
		if errors.Is(err, github.ErrNotFound) {
			return nil, fmt.Errorf("%s has no pull request %d", src.Repository, number)
		}
	} else {
		var prs []github.PullRequest
		prs, err = c.GitHub.PullsFrom(ctx, token, src.Repository, Branch(name))
		if err == nil && len(prs) == 0 {
			return nil, fmt.Errorf("%s has no pull request from %s: create_toolchain opens one, or name its number as pull", src.Repository, Branch(name))
		}
		if err == nil {
			pr = prs[0]
		}
	}
	if errors.Is(err, github.ErrNotFound) {
		return nil, fmt.Errorf("the toolchain-manager's GitHub App does not reach %s with your sign-in: it is not installed there, or you have no access", src.Repository)
	}
	if err != nil {
		return nil, err
	}
	b.Pull, b.PullURL = pr.Number, pr.HTMLURL
	switch {
	case pr.Merged():
		b.Commit = pr.MergeCommitSHA
		return b, c.merged(ctx, token, src, b)
	case pr.State != "open":
		b.State = StateClosed
		return b, nil
	}
	b.Commit = pr.Head.SHA
	if err := c.jobs(ctx, token, b); err != nil {
		return nil, err
	}
	b.State = b.fromJobs(StateChecking, StateChecked)
	return b, nil
}

// merged reads a merged pull request's release: the toolchain's tag at the
// merge commit with its GitHub Release, else the merge commit's jobs.
func (c *Catalog) merged(ctx context.Context, token string, src sources.Source, b *Build) error {
	tags, err := c.GitHub.TagsAt(ctx, token, src.Repository, b.Toolchain+"/v", b.Commit)
	if err != nil && !errors.Is(err, github.ErrNotFound) {
		return err
	}
	if err := c.jobs(ctx, token, b); err != nil {
		return err
	}
	for _, tag := range tags {
		m := releaseTag.FindStringSubmatch(tag)
		if m == nil || m[1] != b.Toolchain {
			continue
		}
		r, err := c.GitHub.ReleaseByTag(ctx, token, src.Repository, tag)
		if errors.Is(err, github.ErrNotFound) || (err == nil && r.Draft) {
			// Tagged, its release not yet created.
			b.State = StateBuilding
			return nil
		}
		if err != nil {
			return err
		}
		b.State = StatePublished
		b.Version = m[2]
		b.Image = src.Registry + "/" + b.Toolchain + ":" + strings.TrimPrefix(m[2], "v")
		b.ReleaseURL = r.HTMLURL
		return nil
	}
	b.State = b.fromJobs(StateBuilding, StateUnreleased)
	return nil
}

func (c *Catalog) jobs(ctx context.Context, token string, b *Build) error {
	jobs, err := c.GitHub.Jobs(ctx, token, b.Repository, b.Commit)
	if err != nil {
		return err
	}
	b.Jobs = b.Jobs[:0]
	for _, j := range jobs {
		if j.State != github.JobSkipped {
			b.Jobs = append(b.Jobs, j)
		}
	}
	return nil
}

// fromJobs is the state the jobs make: failed (Log the first failed job's),
// queued before any started, running while any runs or waits, else done.
func (b *Build) fromJobs(running, done string) string {
	started := false
	for _, j := range b.Jobs {
		if j.State == github.JobFailed {
			b.Log = j.URL
			return StateFailed
		}
	}
	for _, j := range b.Jobs {
		if j.State != github.JobQueued {
			started = true
		}
	}
	if !started {
		return StateQueued
	}
	for _, j := range b.Jobs {
		if j.State == github.JobQueued || j.State == github.JobRunning {
			return running
		}
	}
	return done
}

func (b *Build) message() string {
	what := fmt.Sprintf("toolchain %s, %s", b.Toolchain, b.PullURL)
	switch b.State {
	case StateQueued:
		return what + ": queued, no CI job started yet"
	case StateChecking:
		return what + ": its checks run, the toolchain contract among them"
	case StateChecked:
		return what + ": every check passed; merging it publishes the toolchain"
	case StateBuilding:
		return what + ": merged, the release builds, checks and publishes it"
	case StatePublished:
		return fmt.Sprintf("%s: published %s, image %s", what, b.Version, b.Image)
	case StateFailed:
		return fmt.Sprintf("%s: failed, the log: %s", what, b.Log)
	case StateUnreleased:
		return what + ": merged and every job passed, but no release of it at the merge commit: its directory did not change, or the release did not tag it"
	case StateClosed:
		return what + ": closed without a merge"
	}
	return what + ": " + b.State
}
