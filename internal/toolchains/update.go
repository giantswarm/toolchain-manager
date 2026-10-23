package toolchains

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/identity"
	"github.com/giantswarm/toolchain-manager/internal/sources"
	"github.com/giantswarm/toolchain-manager/internal/toolchain"
	"github.com/giantswarm/toolchain-manager/internal/updates"
)

// An update is a pull request with new pins, opened as the person on the
// branch toolchain-update/<name>: the Dockerfile the manager wrote, its
// versions changed and nothing else. It is reviewed like any pull request
// of its source: GitHub's auto-merge, armed by the person, merges it once
// the source's required reviews and checks, the toolchain contract check
// among them, pass. The manager never reviews it.

// UpdateBranch is the branch update_toolchain opens a toolchain's update
// from.
func UpdateBranch(name string) string { return "toolchain-update/" + name }

// watch hands the newest offered version of t to the background job and
// marks t's version with its outdated notification.
func (c *Catalog) watch(ctx context.Context, t *Toolchain) {
	if c.Updates == nil {
		return
	}
	if o, ok := t.offered(); ok {
		if r, ok := c.declarations.Load(t.Repository + "@" + t.Name + "/" + o.Version); ok {
			c.Updates.Observe(ctx, updates.Watched{
				Source: t.Source, Repository: t.Repository, Toolchain: t.Name, Version: o.Version, ReleaseURL: o.ReleaseURL,
				Team: t.team, SlackChannel: t.channel, Spec: r.(released).spec,
			})
		}
	}
	t.Outdated = c.Updates.Mark(ctx, t.Repository, t.Name, t.Version)
}

// UpdateResult is update_toolchain's answer.
type UpdateResult struct {
	Update *Update `json:"update,omitempty"`
	Answer
}

// Update is an update's pull request.
type Update struct {
	Toolchain  string           `json:"toolchain"`
	Source     string           `json:"source"`
	Repository string           `json:"repository"`
	From       string           `json:"from"`
	Changes    []updates.Change `json:"changes"`
	Major      bool             `json:"major"`
	Pull       int              `json:"pull"`
	PullURL    string           `json:"pullURL"`
	Branch     string           `json:"branch"`
	// AutoMerge: GitHub merges it once its required reviews and checks
	// pass.
	AutoMerge bool `json:"autoMerge"`
	// Note is why auto-merge is not armed.
	Note string `json:"note,omitempty"`
}

// Update opens a pull request, as the person, that updates the newest
// offered version of toolchain name to the new versions of its outdated
// pins, every one or those only names.
func (c *Catalog) Update(ctx context.Context, ref, name string, only []string) (UpdateResult, error) {
	if c.Updater == nil {
		return UpdateResult{}, errors.New("updates are not served")
	}
	t, answer, err := c.find(ctx, name, ref)
	if err != nil || answer.Status != "" {
		return UpdateResult{Answer: answer}, err
	}
	if _, ok := t.offered(); !ok {
		return UpdateResult{}, fmt.Errorf("toolchain %s is deprecated: restore it before updating it", t.Name)
	}
	src, err := c.one(ctx, t.Repository)
	if err != nil {
		return UpdateResult{}, err
	}
	token, ok := identity.Grant(ctx)
	if !ok {
		return UpdateResult{Answer: c.grantRequired()}, nil
	}
	tag := t.Name + "/" + t.Version
	path := dockerfilePath(t.Name)
	current, err := c.GitHub.File(ctx, token, t.Repository, path, tag)
	if errors.Is(err, github.ErrRefused) {
		return UpdateResult{Answer: c.grantRequired()}, nil
	}
	if err != nil {
		return UpdateResult{}, fmt.Errorf("read %s at %s in %s: %w", path, tag, t.Repository, err)
	}
	spec, err := toolchain.Read(current)
	if errors.Is(err, toolchain.ErrHandWritten) {
		return UpdateResult{}, fmt.Errorf("toolchain %s %s is hand-written: the manager does not edit it, its source's Renovate or a person updates it", t.Name, t.Version)
	}
	if err != nil {
		return UpdateResult{}, err
	}
	plan, err := c.Updater.Plan(ctx, spec, only)
	if errors.Is(err, updates.ErrUpToDate) {
		return UpdateResult{Answer: Answer{Message: fmt.Sprintf("toolchain %s %s is %v", t.Name, t.Version, err)}}, nil
	}
	if err != nil {
		return UpdateResult{}, err
	}
	if was, now := Declare(current).Egress, Declare(plan.Dockerfile).Egress; !slices.Equal(was, now) {
		return UpdateResult{}, fmt.Errorf("the update would change toolchain %s's declared egress (%s → %s): an update changes versions only", t.Name, strings.Join(was, ","), strings.Join(now, ","))
	}
	u, err := c.openUpdate(ctx, token, src, t, current, plan)
	if err != nil {
		return UpdateResult{}, err
	}
	if err := c.GitHub.EnableAutoMerge(ctx, token, u.nodeID); err != nil {
		u.Note = fmt.Sprintf("auto-merge is not armed (%v): merge it once its required reviews and checks pass", err)
	} else {
		u.AutoMerge = true
	}
	return UpdateResult{Update: &u.Update, Answer: Answer{Message: u.message()}}, nil
}

type openedUpdate struct {
	Update
	nodeID string
}

func (u *openedUpdate) message() string {
	msg := fmt.Sprintf("opened %s updating toolchain %s %s: %s", u.PullURL, u.Toolchain, u.From, changeList(u.Changes))
	if u.AutoMerge {
		msg += "; it merges once its required reviews and checks, the toolchain contract check among them, pass, and the merge publishes the next version"
	}
	if u.Note != "" {
		msg += "; " + u.Note
	}
	return msg
}

func (c *Catalog) openUpdate(ctx context.Context, token string, src sources.Source, t Toolchain, released []byte, plan updates.Plan) (*openedUpdate, error) {
	repo, err := c.GitHub.Repository(ctx, token, src.Repository)
	if errors.Is(err, github.ErrNotFound) {
		return nil, fmt.Errorf("the toolchain-manager's GitHub App does not reach %s with your sign-in: it is not installed there, or you have no access", src.Repository)
	}
	if err != nil {
		return nil, err
	}
	if !repo.Writable() {
		return nil, fmt.Errorf("you cannot push to %s: an update's pull request is opened as you", src.Repository)
	}
	branch := UpdateBranch(t.Name)
	_, err = c.GitHub.Branch(ctx, token, src.Repository, branch)
	exists := err == nil
	if err != nil && !errors.Is(err, github.ErrNotFound) {
		return nil, err
	}
	if exists {
		prs, err := c.GitHub.PullsFrom(ctx, token, src.Repository, branch)
		if err != nil {
			return nil, err
		}
		for _, pr := range prs {
			if pr.State == "open" {
				return nil, fmt.Errorf("toolchain %s has an open update, %s: it merges or is closed first", t.Name, pr.HTMLURL)
			}
		}
	}
	base, err := c.GitHub.Branch(ctx, token, src.Repository, repo.DefaultBranch)
	if err != nil {
		return nil, fmt.Errorf("read %s's %s: %w", src.Repository, repo.DefaultBranch, err)
	}
	path := dockerfilePath(t.Name)
	// The update is of the default branch's Dockerfile: one changed since
	// the release is a change of its own, which merges first.
	head, err := c.GitHub.File(ctx, token, src.Repository, path, repo.DefaultBranch)
	if err != nil {
		return nil, fmt.Errorf("read %s on %s: %w", path, repo.DefaultBranch, err)
	}
	if string(head) != string(released) {
		return nil, fmt.Errorf("%s on %s changed since %s %s: its release comes first, then the update", path, repo.DefaultBranch, t.Name, t.Version)
	}
	if err := c.GitHub.SetBranch(ctx, token, src.Repository, branch, base, !exists); err != nil {
		return nil, fmt.Errorf("branch %s: %w", branch, err)
	}
	sha, err := c.GitHub.FileSHA(ctx, token, src.Repository, path, branch)
	if err != nil {
		return nil, fmt.Errorf("read %s on %s: %w", path, branch, err)
	}
	title := updateTitle(t.Name, plan)
	if err := c.GitHub.PutFile(ctx, token, src.Repository, branch, path, title, sha, plan.Dockerfile); err != nil {
		return nil, fmt.Errorf("commit %s: %w", path, err)
	}
	pr, err := c.GitHub.CreatePull(ctx, token, src.Repository, branch, repo.DefaultBranch, title, updateBody(t, plan))
	if err != nil {
		return nil, fmt.Errorf("open the pull request: %w", err)
	}
	return &openedUpdate{nodeID: pr.NodeID, Update: Update{
		Toolchain: t.Name, Source: src.Name, Repository: src.Repository, From: t.Version,
		Changes: plan.Changes, Major: plan.Major(), Pull: pr.Number, PullURL: pr.HTMLURL, Branch: branch,
	}}, nil
}

// updateTitle is the squash subject, which sets the release's bump: a major
// pin a breaking change, a minor one a feature, patches a fix.
func updateTitle(name string, plan updates.Plan) string {
	kind := "fix"
	switch {
	case plan.Major():
		kind = "feat(" + name + ")!"
	case slices.ContainsFunc(plan.Changes, minor):
		kind = "feat(" + name + ")"
	default:
		kind += "(" + name + ")"
	}
	return kind + ": update " + changeList(plan.Changes)
}

// minor reports whether a change moves a minor version.
func minor(c updates.Change) bool {
	return segment(c.From, 1) != segment(c.To, 1)
}

func segment(v string, i int) string {
	v = strings.TrimLeftFunc(v, func(r rune) bool { return r < '0' || r > '9' })
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' || r == '_' })
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

func changeList(changes []updates.Change) string {
	const shown = 3
	parts := make([]string, 0, shown)
	for i, c := range changes {
		if i == shown {
			parts = append(parts, fmt.Sprintf("and %d more", len(changes)-shown))
			break
		}
		parts = append(parts, c.String())
	}
	return strings.Join(parts, ", ")
}

func updateBody(t Toolchain, plan updates.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Updates the `%s` toolchain from %s, opened with the toolchain-manager's `update_toolchain`: its pins only, its tools, Harnesses and egress unchanged.\n\n", t.Name, t.Version)
	b.WriteString("| | Pinned | Updated to |\n|---|---|---|\n")
	for _, c := range plan.Changes {
		major := ""
		if c.Major {
			major = " (major)"
		}
		fmt.Fprintf(&b, "| %s `%s` | %s | %s%s |\n", c.Kind, c.Name, c.From, c.To, major)
	}
	if plan.Major() {
		b.WriteString("\nA major update: its release is the next major version.\n")
	}
	b.WriteString("\nIt is reviewed like any pull request here, and merges once the required reviews and checks, the toolchain contract check among them, pass.\n")
	return b.String()
}
