// Package sources keeps an installation's toolchain sources: the ones its
// configuration names, approved by being named there, and the ones people
// add, each a ToolchainSource in the manager's namespace that the
// installation's owner approves once. A source is added with the grant of
// the toolchain-manager's GitHub App on its repository, checked with the
// person's own App token, and the Slack channel of the team that approves
// its updates. An approved source's contract-checked toolchains are offered.
package sources

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/giantswarm/toolchain-manager/api/v1alpha1"
	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/identity"
)

// Origin says where a source comes from.
type Origin string

const (
	// OriginConfiguration is a source the installation's configuration
	// names: approved by being named there, removed only there.
	OriginConfiguration Origin = "configuration"
	// OriginAdded is a source a person added with add_source.
	OriginAdded Origin = "added"
)

// Status values of a tool result that cannot go on without the person.
const (
	// StatusGrantRequired: the person has not granted the toolchain-manager's
	// GitHub App in muster; muster answers it with the App's sign-in link.
	StatusGrantRequired = "grant_required"
	// StatusInstallRequired: the App is not installed on the repository, or
	// the person cannot read it; installing it there is the grant.
	StatusInstallRequired = "install_required"
)

// Configured is a source the installation's configuration names.
type Configured struct {
	Repository   string `json:"repository"`
	Registry     string `json:"registry"`
	SlackChannel string `json:"slackChannel,omitempty"`
}

// Owners are the installation's owners, who approve a new source: a caller
// with a verified email among Emails or a group among Groups.
type Owners struct {
	Groups []string
	Emails []string
}

// Owns reports whether id is one of the installation's owners.
func (o Owners) Owns(id *identity.Identity) bool {
	if id == nil {
		return false
	}
	if id.EmailVerified && slices.ContainsFunc(o.Emails, func(e string) bool { return strings.EqualFold(e, id.Email) }) {
		return true
	}
	return slices.ContainsFunc(id.Groups, func(g string) bool { return slices.Contains(o.Groups, g) })
}

// Source is a toolchain source as the tools show it.
type Source struct {
	Name         string `json:"name"`
	Repository   string `json:"repository"`
	URL          string `json:"url"`
	Registry     string `json:"registry"`
	SlackChannel string `json:"slackChannel,omitempty"`
	Origin       Origin `json:"origin"`
	// Phase is Approved or, for an added source the owner has not approved
	// yet, Pending.
	Phase v1alpha1.Phase `json:"phase"`
	// Offered: the installation offers the source's contract-checked
	// toolchains.
	Offered    bool       `json:"offered"`
	AddedBy    string     `json:"addedBy,omitempty"`
	AddedAt    *time.Time `json:"addedAt,omitempty"`
	ApprovedBy string     `json:"approvedBy,omitempty"`
	ApprovedAt *time.Time `json:"approvedAt,omitempty"`
}

// Manager keeps the sources.
type Manager struct {
	// Client reads and writes ToolchainSources in Namespace, as the
	// manager's ServiceAccount.
	Client    client.Client
	Namespace string
	// Configured are the sources the installation's configuration names.
	Configured []Configured
	Owners     Owners
	// GitHub checks the App's grant with the person's token.
	GitHub *github.Client
	// GrantApp is the provider App muster keeps the person's grant of and
	// attaches to every call (the MCPServer's auth.providerApps entry).
	GrantApp string
	// InstallURL is where a repository's owner installs the App.
	InstallURL string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Validate checks the configuration.
func (m *Manager) Validate() error {
	seen := map[string]bool{}
	for _, c := range m.Configured {
		repo, err := NormalizeRepository(c.Repository)
		if err != nil {
			return fmt.Errorf("configured source: %w", err)
		}
		if err := validateRegistry(c.Registry); err != nil {
			return fmt.Errorf("configured source %s: %w", repo, err)
		}
		if seen[strings.ToLower(repo)] {
			return fmt.Errorf("configured source %s is named twice", repo)
		}
		seen[strings.ToLower(repo)] = true
	}
	if m.GrantApp == "" {
		return errors.New("the GitHub App muster keeps the people's grants of is not named")
	}
	return nil
}

// List returns every source, the configured ones first, each group sorted by
// repository.
func (m *Manager) List(ctx context.Context) ([]Source, error) {
	if _, err := caller(ctx); err != nil {
		return nil, err
	}
	return m.list(ctx)
}

func (m *Manager) list(ctx context.Context) ([]Source, error) {
	out := make([]Source, 0, len(m.Configured))
	for _, c := range m.Configured {
		repo, _ := NormalizeRepository(c.Repository)
		out = append(out, Source{
			Name: Name(repo), Repository: repo, URL: "https://github.com/" + repo, Registry: c.Registry,
			SlackChannel: c.SlackChannel, Origin: OriginConfiguration, Phase: v1alpha1.PhaseApproved, Offered: true,
		})
	}
	var list v1alpha1.ToolchainSourceList
	if err := m.Client.List(ctx, &list, client.InNamespace(m.Namespace)); err != nil {
		return nil, fmt.Errorf("list the added sources: %w", err)
	}
	added := make([]Source, 0, len(list.Items))
	for i := range list.Items {
		added = append(added, view(&list.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repository < out[j].Repository })
	sort.Slice(added, func(i, j int) bool { return added[i].Repository < added[j].Repository })
	return append(out, added...), nil
}

// Offered returns the sources whose toolchains are offered: the configured
// ones and the approved added ones, in List's order.
func (m *Manager) Offered(ctx context.Context) ([]Source, error) {
	list, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, s := range list {
		if s.Offered {
			out = append(out, s)
		}
	}
	return out, nil
}

// AddInput is add_source's input.
type AddInput struct {
	// Repository is owner/name or the repository's GitHub URL.
	Repository string `json:"repository"`
	// Registry is the image repository prefix the source's toolchains
	// publish under.
	Registry string `json:"registry"`
	// SlackChannel is the owning team's channel that approves the source's
	// updates.
	SlackChannel string `json:"slackChannel"`
}

// AddResult is add_source's answer: the source, or what the person does
// first (Status, Apps, InstallURL, Message).
type AddResult struct {
	Source *Source `json:"source,omitempty"`
	// Status is StatusGrantRequired or StatusInstallRequired when the source
	// was not added.
	Status string `json:"status,omitempty"`
	// Apps names the App muster asks the person to grant, with
	// StatusGrantRequired.
	Apps []string `json:"apps,omitempty"`
	// InstallURL is where the App is installed, with StatusInstallRequired.
	InstallURL string `json:"installURL,omitempty"`
	Message    string `json:"message"`
}

// Add adds a source for the caller: the App's installation must grant the
// repository and the caller must be able to push to it. An owner's source is
// approved by adding it; any other waits for an owner's approve_source.
func (m *Manager) Add(ctx context.Context, in AddInput) (AddResult, error) {
	id, err := caller(ctx)
	if err != nil {
		return AddResult{}, err
	}
	repo, err := NormalizeRepository(in.Repository)
	if err != nil {
		return AddResult{}, err
	}
	if err := validateRegistry(in.Registry); err != nil {
		return AddResult{}, err
	}
	channel, err := normalizeChannel(in.SlackChannel)
	if err != nil {
		return AddResult{}, err
	}
	existing, err := m.list(ctx)
	if err != nil {
		return AddResult{}, err
	}
	if i := slices.IndexFunc(existing, func(s Source) bool { return strings.EqualFold(s.Repository, repo) }); i >= 0 {
		return AddResult{}, fmt.Errorf("%s is a source already (%s, %s)", repo, existing[i].Origin, existing[i].Phase)
	}

	token, ok := identity.Grant(ctx)
	if !ok {
		return m.grantRequired(), nil
	}
	r, err := m.GitHub.Repository(ctx, token, repo)
	switch {
	case errors.Is(err, github.ErrRefused):
		return m.grantRequired(), nil
	case errors.Is(err, github.ErrNotFound):
		return AddResult{
			Status:     StatusInstallRequired,
			InstallURL: m.InstallURL,
			Message: fmt.Sprintf("the toolchain-manager's GitHub App does not reach %s with your sign-in: its owner installs the App on it (%s), which grants the manager what it needs, and you need access to it; then add it again",
				repo, m.installHint()),
		}, nil
	case err != nil:
		return AddResult{}, fmt.Errorf("read %s on GitHub: %w", repo, err)
	}
	if !r.Writable() {
		return AddResult{}, fmt.Errorf("you cannot push to %s: a source is added by someone who maintains its repository", r.FullName)
	}

	now := metav1.NewTime(m.now())
	src := &v1alpha1.ToolchainSource{
		ObjectMeta: metav1.ObjectMeta{Name: Name(r.FullName), Namespace: m.Namespace},
		Spec: v1alpha1.ToolchainSourceSpec{
			Repository: r.FullName, URL: r.HTMLURL, Registry: in.Registry, SlackChannel: channel,
			AddedBy: id.String(), AddedBySubject: id.Subject,
		},
	}
	if err := m.Client.Create(ctx, src); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return AddResult{}, fmt.Errorf("a source named %s exists already for another repository", src.Name)
		}
		return AddResult{}, fmt.Errorf("add %s: %w", r.FullName, err)
	}
	src.Status = v1alpha1.ToolchainSourceStatus{Phase: v1alpha1.PhasePending}
	msg := fmt.Sprintf("added %s; it waits for the installation owner's approval (approve_source) before its toolchains are offered", r.FullName)
	if m.Owners.Owns(id) {
		src.Status = v1alpha1.ToolchainSourceStatus{Phase: v1alpha1.PhaseApproved, ApprovedBy: id.String(), ApprovedAt: &now}
		msg = fmt.Sprintf("added %s, approved by you as the installation's owner: its contract-checked toolchains are offered", r.FullName)
	}
	if err := m.Client.Status().Update(ctx, src); err != nil {
		return AddResult{}, fmt.Errorf("record %s's review: %w", r.FullName, err)
	}
	s := view(src)
	return AddResult{Source: &s, Message: msg}, nil
}

// Approve approves an added source; only an installation owner may.
func (m *Manager) Approve(ctx context.Context, ref string) (Source, error) {
	id, err := caller(ctx)
	if err != nil {
		return Source{}, err
	}
	if !m.Owners.Owns(id) {
		return Source{}, fmt.Errorf("%s is not an owner of this installation: an owner approves a source", id)
	}
	src, err := m.added(ctx, ref)
	if err != nil {
		return Source{}, err
	}
	if src.Status.Phase == v1alpha1.PhaseApproved {
		return view(src), nil
	}
	now := metav1.NewTime(m.now())
	src.Status = v1alpha1.ToolchainSourceStatus{Phase: v1alpha1.PhaseApproved, ApprovedBy: id.String(), ApprovedAt: &now}
	if err := m.Client.Status().Update(ctx, src); err != nil {
		return Source{}, fmt.Errorf("approve %s: %w", src.Spec.Repository, err)
	}
	return view(src), nil
}

// Remove removes an added source; the person who added it or an owner may.
func (m *Manager) Remove(ctx context.Context, ref string) (Source, error) {
	id, err := caller(ctx)
	if err != nil {
		return Source{}, err
	}
	src, err := m.added(ctx, ref)
	if err != nil {
		return Source{}, err
	}
	if (src.Spec.AddedBySubject == "" || src.Spec.AddedBySubject != id.Subject) && !m.Owners.Owns(id) {
		return Source{}, fmt.Errorf("%s was added by %s: they or an installation owner remove it", src.Spec.Repository, src.Spec.AddedBy)
	}
	if err := m.Client.Delete(ctx, src, client.Preconditions{UID: &src.UID}); err != nil && !apierrors.IsNotFound(err) {
		return Source{}, fmt.Errorf("remove %s: %w", src.Spec.Repository, err)
	}
	return view(src), nil
}

// added finds the added source ref names, by name or repository; a
// configured one is refused with where it is removed.
func (m *Manager) added(ctx context.Context, ref string) (*v1alpha1.ToolchainSource, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("name the source: its name or repository, as list_sources shows it")
	}
	repo, repoErr := NormalizeRepository(ref)
	for _, c := range m.Configured {
		r, _ := NormalizeRepository(c.Repository)
		if strings.EqualFold(ref, Name(r)) || (repoErr == nil && strings.EqualFold(repo, r)) {
			return nil, fmt.Errorf("%s is named by the installation's configuration: it changes there", r)
		}
	}
	var list v1alpha1.ToolchainSourceList
	if err := m.Client.List(ctx, &list, client.InNamespace(m.Namespace)); err != nil {
		return nil, fmt.Errorf("list the added sources: %w", err)
	}
	for i := range list.Items {
		s := &list.Items[i]
		if s.Name == ref || (repoErr == nil && strings.EqualFold(s.Spec.Repository, repo)) {
			return s, nil
		}
	}
	return nil, fmt.Errorf("no source %s: list_sources shows them", ref)
}

func (m *Manager) grantRequired() AddResult {
	return AddResult{
		Status: StatusGrantRequired,
		Apps:   []string{m.GrantApp},
		Message: fmt.Sprintf("grant the toolchain-manager's GitHub App once in muster (core_auth_login app=%s): the manager checks with your sign-in that the App's installation grants the repository",
			m.GrantApp),
	}
}

func (m *Manager) installHint() string {
	if m.InstallURL == "" {
		return "ask the installation's owner for the App"
	}
	return m.InstallURL
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func caller(ctx context.Context) (*identity.Identity, error) {
	id, ok := identity.FromContext(ctx)
	if !ok || id.Subject == "" {
		return nil, errors.New("no caller: the source tools act for the person muster forwards")
	}
	return id, nil
}

func view(s *v1alpha1.ToolchainSource) Source {
	phase := s.Status.Phase
	if phase == "" {
		phase = v1alpha1.PhasePending
	}
	out := Source{
		Name: s.Name, Repository: s.Spec.Repository, URL: s.Spec.URL, Registry: s.Spec.Registry,
		SlackChannel: s.Spec.SlackChannel, Origin: OriginAdded, Phase: phase, Offered: phase == v1alpha1.PhaseApproved,
		AddedBy: s.Spec.AddedBy, ApprovedBy: s.Status.ApprovedBy,
	}
	if !s.CreationTimestamp.IsZero() {
		t := s.CreationTimestamp.UTC()
		out.AddedAt = &t
	}
	if s.Status.ApprovedAt != nil {
		t := s.Status.ApprovedAt.UTC()
		out.ApprovedAt = &t
	}
	return out
}

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	registryPattern   = regexp.MustCompile(`^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)*$`)
	channelPattern    = regexp.MustCompile(`^[a-z0-9_-]{1,80}$`)
	nameInvalid       = regexp.MustCompile(`[^a-z0-9]+`)
)

// NormalizeRepository turns owner/name or a github.com URL of it into
// owner/name.
func NormalizeRepository(s string) (string, error) {
	r := strings.TrimSpace(s)
	for _, p := range []string{"https://github.com/", "http://github.com/", "github.com/"} {
		if strings.HasPrefix(strings.ToLower(r), p) {
			r = r[len(p):]
			break
		}
	}
	r = strings.TrimSuffix(strings.TrimSuffix(r, "/"), ".git")
	if !repositoryPattern.MatchString(r) {
		return "", fmt.Errorf("repository %q: want owner/name or its https://github.com/ URL", s)
	}
	return r, nil
}

// Name is a source's object name: its repository, lowercased, every run of
// other characters (dashes included) one dash.
func Name(repository string) string {
	n := strings.Trim(nameInvalid.ReplaceAllString(strings.ToLower(repository), "-"), "-")
	if len(n) > 63 {
		n = strings.TrimRight(n[:63], "-")
	}
	return n
}

func validateRegistry(r string) error {
	if !registryPattern.MatchString(r) || !strings.Contains(strings.SplitN(r, "/", 2)[0], ".") || !strings.Contains(r, "/") {
		return fmt.Errorf("registry %q: want a registry host and repository prefix, such as registry.example.com/team/toolchains", r)
	}
	return nil
}

// normalizeChannel takes a Slack channel name with or without its #.
func normalizeChannel(c string) (string, error) {
	n := strings.TrimPrefix(strings.TrimSpace(c), "#")
	if !channelPattern.MatchString(n) {
		return "", fmt.Errorf("slackChannel %q: want the channel's name, such as #team-toolchains, of the team that approves the source's updates", c)
	}
	return "#" + n, nil
}
