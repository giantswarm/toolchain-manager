package toolchains

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/giantswarm/toolchain-manager/api/v1alpha1"
	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/identity"
)

// A deprecated toolchain, or a version of it, leaves the offered list: it is
// a ToolchainDeprecation in the manager's namespace. Its releases and images
// stay, so a Session that holds its digest keeps resuming on it; a new
// Session cannot name it, since agent-manager starts only what the list
// carries. Before it is deprecated the person sees who uses it: the agents
// whose default toolchains name it and the Sessions that hold its digest, as
// agent-manager answers them for the person.

// UsageReader names the agents and Sessions that use a toolchain, or one of
// its versions (vX.Y.Z), asked as the person whose Dex ID token bearer is.
type UsageReader interface {
	Users(ctx context.Context, bearer, name, version string) (Usage, error)
}

// Usage is who uses a toolchain.
type Usage struct {
	// Agents are the agents whose default toolchains name it.
	Agents []AgentUse `json:"agents"`
	// Sessions are the Sessions that hold its digest.
	Sessions []SessionUse `json:"sessions"`
	// Unreadable are the places whose agents or Sessions could not be read
	// as the person.
	Unreadable []string `json:"unreadable,omitempty"`
}

// AgentUse is an agent whose default toolchains name the toolchain.
type AgentUse struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Toolchain is its default toolchain entry, name[@version].
	Toolchain string `json:"toolchain"`
}

// SessionUse is a Session that holds the toolchain's digest.
type SessionUse struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
	Agent     string `json:"agent"`
	// Image is the digest-pinned image the Session mounts.
	Image string `json:"image"`
	State string `json:"state,omitempty"`
}

// DeprecateInput is deprecate_toolchain's input.
type DeprecateInput struct {
	Name string
	// Version is one version, vX.Y.Z; empty is the whole toolchain.
	Version string
	// Source names the source when two publish the toolchain.
	Source string
	Reason string
	// Confirm deprecates; without it the answer only names the users.
	Confirm bool
	// Restore takes the deprecation back.
	Restore bool
}

// DeprecateResult is deprecate_toolchain's answer.
type DeprecateResult struct {
	Toolchain  string `json:"toolchain,omitempty"`
	Version    string `json:"version,omitempty"`
	Repository string `json:"repository,omitempty"`
	// Deprecated says whether it is deprecated now.
	Deprecated bool `json:"deprecated"`
	// Changed says whether this call changed it.
	Changed bool   `json:"changed"`
	Usage   *Usage `json:"usage,omitempty"`
	Answer
}

// Deprecate deprecates the toolchain in.Name, or its version in.Version,
// once in.Confirm says so: before, it answers who uses it. Only a person who
// can push to its source, or an installation owner, deprecates it or takes
// the deprecation back.
func (c *Catalog) Deprecate(ctx context.Context, in DeprecateInput) (DeprecateResult, error) {
	if c.Users == nil {
		return DeprecateResult{}, errors.New("this installation does not reach agent-manager through muster, so who uses a toolchain cannot be named")
	}
	id, ok := identity.FromContext(ctx)
	if !ok {
		return DeprecateResult{}, errors.New("no authenticated caller")
	}
	t, answer, err := c.find(ctx, in.Name, in.Source)
	if err != nil || answer.Status != "" {
		return DeprecateResult{Answer: answer}, err
	}
	version := normalizeVersion(in.Version)
	if version != "" {
		if _, ok := t.releases[version]; !ok {
			return DeprecateResult{}, fmt.Errorf("toolchain %s has no published version %s; it has %s", t.Name, version, strings.Join(t.versionNames(), ", "))
		}
	}
	res := DeprecateResult{Toolchain: t.Name, Version: version, Repository: t.Repository}
	what := t.Name
	if version != "" {
		what += " " + version
	}
	if version != "" && t.Deprecated != nil && !in.Restore {
		return DeprecateResult{}, fmt.Errorf("toolchain %s is deprecated with every version already, by %s", t.Name, t.Deprecated.By)
	}
	if err := c.mayDeprecate(ctx, id, t.Repository); err != nil {
		return DeprecateResult{}, err
	}
	key := client.ObjectKey{Namespace: c.Sources.Namespace, Name: deprecationName(t.Repository, t.Name, version)}
	var existing v1alpha1.ToolchainDeprecation
	err = c.Sources.Client.Get(ctx, key, &existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return DeprecateResult{}, fmt.Errorf("read the deprecation of %s: %w", what, err)
	}
	found := err == nil

	if in.Restore {
		res.Deprecated = false
		if !found {
			res.Message = fmt.Sprintf("%s of %s is not deprecated", what, t.Repository)
			return res, nil
		}
		if err := c.Sources.Client.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
			return DeprecateResult{}, fmt.Errorf("take back the deprecation of %s: %w", what, err)
		}
		res.Changed = true
		res.Message = fmt.Sprintf("%s of %s is offered again", what, t.Repository)
		return res, nil
	}
	if found {
		res.Deprecated = true
		res.Message = fmt.Sprintf("%s of %s is deprecated already, by %s", what, t.Repository, existing.Spec.DeprecatedBy)
		return res, nil
	}

	bearer, _ := identity.IDToken(ctx)
	usage, err := c.Users.Users(ctx, bearer, t.Name, version)
	if err != nil {
		return DeprecateResult{}, fmt.Errorf("who uses %s: %w", what, err)
	}
	res.Usage = &usage
	users := fmt.Sprintf("%d agents name it as a default toolchain and %d Sessions hold its digest", len(usage.Agents), len(usage.Sessions))
	if len(usage.Unreadable) > 0 {
		users += fmt.Sprintf(" (%d places could not be read as you)", len(usage.Unreadable))
	}
	if !in.Confirm {
		res.Message = fmt.Sprintf("%s of %s is offered: %s. Deprecating it takes it out of list_toolchains, so no new Session starts on it; its image stays, so those Sessions keep resuming. Call again with confirm to deprecate it", what, t.Repository, users)
		return res, nil
	}
	dep := &v1alpha1.ToolchainDeprecation{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: v1alpha1.ToolchainDeprecationSpec{
			Repository: t.Repository, Toolchain: t.Name, Version: version,
			Reason: strings.TrimSpace(in.Reason), DeprecatedBy: id.String(),
		},
	}
	if err := c.Sources.Client.Create(ctx, dep); err != nil && !apierrors.IsAlreadyExists(err) {
		return DeprecateResult{}, fmt.Errorf("deprecate %s: %w", what, err)
	}
	res.Deprecated, res.Changed = true, true
	res.Message = fmt.Sprintf("deprecated %s of %s: it left list_toolchains; %s, and they keep resuming on its image", what, t.Repository, users)
	return res, nil
}

// mayDeprecate allows an installation owner, and a person who can push to
// the source, as their App token shows.
func (c *Catalog) mayDeprecate(ctx context.Context, id *identity.Identity, repository string) error {
	if c.Sources.Owners.Owns(id) {
		return nil
	}
	token, _ := identity.Grant(ctx)
	repo, err := c.GitHub.Repository(ctx, token, repository)
	switch {
	case errors.Is(err, github.ErrNotFound):
		return fmt.Errorf("the toolchain-manager's GitHub App does not reach %s with your sign-in: it is not installed there, or you have no access", repository)
	case err != nil:
		return err
	case !repo.Writable():
		return fmt.Errorf("only a person who can push to %s, or an installation owner, deprecates its toolchains", repository)
	}
	return nil
}

// deprecationName is a deprecation's object name: one per toolchain of a
// repository, and per version.
func deprecationName(repository, name, version string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(repository)))
	scope := "all"
	if version != "" {
		scope = version
	}
	return name + "-" + scope + "-" + hex.EncodeToString(sum[:4])
}

// deprecationSet is the installation's deprecations, by repository,
// toolchain and version ("" the whole toolchain).
type deprecationSet map[string]*Deprecated

func deprecationKey(repository, name, version string) string {
	return strings.ToLower(repository) + "/" + name + "@" + version
}

// deprecations reads the installation's deprecations.
func (c *Catalog) deprecations(ctx context.Context) (deprecationSet, error) {
	var list v1alpha1.ToolchainDeprecationList
	if err := c.Sources.Client.List(ctx, &list, client.InNamespace(c.Sources.Namespace)); err != nil {
		return nil, fmt.Errorf("list the deprecations: %w", err)
	}
	out := deprecationSet{}
	for _, d := range list.Items {
		at := d.CreationTimestamp.UTC()
		dep := &Deprecated{By: d.Spec.DeprecatedBy, Reason: d.Spec.Reason}
		if !at.IsZero() {
			dep.At = &at
		}
		out[deprecationKey(d.Spec.Repository, d.Spec.Toolchain, d.Spec.Version)] = dep
	}
	return out, nil
}

// mark marks t and its versions deprecated as the set has them.
func (s deprecationSet) mark(t Toolchain) Toolchain {
	t.Deprecated = s[deprecationKey(t.Repository, t.Name, "")]
	for i := range t.Versions {
		t.Versions[i].Deprecated = s[deprecationKey(t.Repository, t.Name, t.Versions[i].Version)]
	}
	return t
}
