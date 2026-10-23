package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/giantswarm/toolchain-manager/internal/toolchain"
)

// Watched is a toolchain the background job checks: the newest offered
// version of a published toolchain, as a person's call read it from its
// source, with its pins when the manager wrote its Dockerfile.
type Watched struct {
	Source     string `json:"source"`
	Repository string `json:"repository"`
	Toolchain  string `json:"toolchain"`
	Version    string `json:"version"`
	ReleaseURL string `json:"releaseURL,omitempty"`
	// Team and SlackChannel are the source's owning team and its channel,
	// where the outdated notice goes.
	Team         string `json:"team,omitempty"`
	SlackChannel string `json:"slackChannel,omitempty"`
	// Spec is the Dockerfile's spec; nil for a hand-written one, which the
	// manager does not check.
	Spec *toolchain.Spec `json:"spec,omitempty"`
}

// Record is the state the job keeps of a watched toolchain.
type Record struct {
	Watched
	// Outdated is the last check's finding, nil before the first.
	Outdated  *Outdated  `json:"outdated,omitempty"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	// Error is why the last check failed.
	Error string `json:"error,omitempty"`
	// Notified is the outdated pins the team's channel was told of.
	Notified string `json:"notified,omitempty"`
}

// Outdated is a toolchain's outdated mark: its pins with a newer version,
// and the notification that names them.
type Outdated struct {
	Status
	// Message is the notification: the outdated pins and their new versions.
	Message   string     `json:"message"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
}

// Notifier tells a source's owning team, in its Slack channel, that a
// toolchain is outdated.
type Notifier interface {
	Notice(ctx context.Context, team, channel, text, link string) error
}

// Watch is the background job: it keeps the state of every watched
// toolchain's pins in a ConfigMap of the manager's namespace, checks them
// every Interval, marks a toolchain outdated with a notification and tells
// the owning team's channel once per set of outdated pins. It changes
// nothing in a source.
type Watch struct {
	Updater *Updater
	// Client keeps the ConfigMap Name in Namespace.
	Client    client.Client
	Namespace string
	Name      string
	// Notifier tells the team; nil leaves the notice to the tools.
	Notifier Notifier
	Interval time.Duration
	Log      *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu      sync.Mutex
	records map[string]*Record
	loaded  bool
	kick    chan struct{}
}

// Key is a toolchain's record key: a ConfigMap data key.
func Key(repository, name string) string {
	return strings.NewReplacer("/", ".").Replace(strings.ToLower(repository)) + "." + name
}

// Observe records the newest offered version of a toolchain; a version
// other than the recorded one replaces its record and is checked at once.
func (w *Watch) Observe(ctx context.Context, t Watched) {
	w.mu.Lock()
	if err := w.loadLocked(ctx); err != nil {
		w.mu.Unlock()
		w.log().Warn("updates: load the state", "error", err)
		return
	}
	key := Key(t.Repository, t.Toolchain)
	r, ok := w.records[key]
	if ok && r.Version == t.Version && r.Team == t.Team && r.SlackChannel == t.SlackChannel && r.ReleaseURL == t.ReleaseURL {
		w.mu.Unlock()
		return
	}
	if ok && r.Version == t.Version {
		r.Watched = t
	} else {
		w.records[key] = &Record{Watched: t}
	}
	err := w.saveLocked(ctx)
	w.mu.Unlock()
	if err != nil {
		w.log().Warn("updates: save the state", "toolchain", t.Toolchain, "error", err)
	}
	if !ok || r.Version != t.Version {
		w.wake()
	}
}

// Mark is the outdated mark of a toolchain at version: nil when it is up to
// date, not checked yet, or the record is of another version.
func (w *Watch) Mark(ctx context.Context, repository, name, version string) *Outdated {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.loadLocked(ctx); err != nil {
		return nil
	}
	r, ok := w.records[Key(repository, name)]
	if !ok || r.Version != version || r.Outdated == nil || len(r.Outdated.Outdated) == 0 {
		return nil
	}
	o := *r.Outdated
	return &o
}

// Run checks every watched toolchain each Interval until ctx ends, and at
// once for a newly observed version.
func (w *Watch) Run(ctx context.Context) {
	w.mu.Lock()
	if w.kick == nil {
		w.kick = make(chan struct{}, 1)
	}
	kick := w.kick
	w.mu.Unlock()
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		w.Round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
	}
}

func (w *Watch) wake() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.kick == nil {
		return
	}
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// Round checks every watched toolchain once and notifies the teams of the
// newly outdated ones.
func (w *Watch) Round(ctx context.Context) {
	w.mu.Lock()
	if err := w.loadLocked(ctx); err != nil {
		w.mu.Unlock()
		w.log().Warn("updates: load the state", "error", err)
		return
	}
	todo := make([]Record, 0, len(w.records))
	for _, r := range w.records {
		todo = append(todo, *r)
	}
	w.mu.Unlock()
	sort.Slice(todo, func(i, j int) bool {
		return Key(todo[i].Repository, todo[i].Toolchain) < Key(todo[j].Repository, todo[j].Toolchain)
	})

	for _, r := range todo {
		if r.Spec == nil {
			continue
		}
		now := w.now().UTC()
		st, err := w.Updater.Check(ctx, *r.Spec)
		r.CheckedAt = &now
		r.Error = ""
		if err != nil {
			r.Error = err.Error()
			w.log().Warn("updates: check", "toolchain", r.Toolchain, "repository", r.Repository, "error", err)
		} else {
			r.Outdated = &Outdated{Status: st, Message: message(r.Watched, st), CheckedAt: &now}
			w.notify(ctx, &r)
		}
		w.mu.Lock()
		// A record replaced meanwhile by a newer version stays as it is.
		if cur, ok := w.records[Key(r.Repository, r.Toolchain)]; ok && cur.Version == r.Version {
			*cur = r
		}
		err = w.saveLocked(ctx)
		w.mu.Unlock()
		if err != nil {
			w.log().Warn("updates: save the state", "toolchain", r.Toolchain, "error", err)
		}
	}
}

// notify tells the team of a set of outdated pins it was not told of.
func (w *Watch) notify(ctx context.Context, r *Record) {
	fp := fingerprint(r.Outdated.Outdated)
	if fp == "" {
		r.Notified = ""
		return
	}
	if fp == r.Notified || w.Notifier == nil {
		return
	}
	if r.Team == "" || r.SlackChannel == "" {
		w.log().Info("updates: outdated, no channel to tell", "toolchain", r.Toolchain, "repository", r.Repository, "outdated", fp)
		return
	}
	if err := w.Notifier.Notice(ctx, r.Team, r.SlackChannel, slackText(r.Watched, r.Outdated.Status), r.ReleaseURL); err != nil {
		w.log().Warn("updates: notify the team", "toolchain", r.Toolchain, "channel", r.SlackChannel, "error", err)
		return
	}
	r.Notified = fp
	w.log().Info("updates: outdated, team notified", "toolchain", r.Toolchain, "repository", r.Repository, "channel", r.SlackChannel, "outdated", fp)
}

func fingerprint(changes []Change) string {
	parts := make([]string, len(changes))
	for i, c := range changes {
		parts[i] = c.Kind + ":" + c.Name + "=" + c.To
	}
	return strings.Join(parts, ",")
}

func message(t Watched, st Status) string {
	if len(st.Outdated) == 0 {
		return fmt.Sprintf("%s %s is up to date", t.Toolchain, t.Version)
	}
	parts := make([]string, len(st.Outdated))
	for i, c := range st.Outdated {
		parts[i] = c.String()
		if c.Major {
			parts[i] += " (major)"
		}
	}
	return fmt.Sprintf("%s %s is outdated: %s; update_toolchain opens the pull request with the new pins", t.Toolchain, t.Version, strings.Join(parts, ", "))
}

func slackText(t Watched, st Status) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*Toolchain `%s` %s is outdated* (source `%s`):\n", t.Toolchain, t.Version, t.Repository)
	for _, c := range st.Outdated {
		fmt.Fprintf(&b, "• %s `%s` %s → %s", c.Kind, c.Name, c.From, c.To)
		if c.Major {
			b.WriteString(" — major")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "The toolchain-manager's `update_toolchain` (name `%s`) opens the pull request with the new pins; a major update asks this channel, a patch or minor one merges once the toolchain contract check is green.", t.Toolchain)
	return b.String()
}

func (w *Watch) loadLocked(ctx context.Context) error {
	if w.loaded {
		return nil
	}
	w.records = map[string]*Record{}
	var cm corev1.ConfigMap
	err := w.Client.Get(ctx, client.ObjectKey{Namespace: w.Namespace, Name: w.Name}, &cm)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("read ConfigMap %s/%s: %w", w.Namespace, w.Name, err)
	default:
		for k, v := range cm.Data {
			var r Record
			if err := json.Unmarshal([]byte(v), &r); err != nil {
				w.log().Warn("updates: drop an unreadable record", "key", k, "error", err)
				continue
			}
			w.records[k] = &r
		}
	}
	w.loaded = true
	return nil
}

func (w *Watch) saveLocked(ctx context.Context) error {
	data := make(map[string]string, len(w.records))
	for k, r := range w.records {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		data[k] = string(b)
	}
	var cm corev1.ConfigMap
	err := w.Client.Get(ctx, client.ObjectKey{Namespace: w.Namespace, Name: w.Name}, &cm)
	if apierrors.IsNotFound(err) {
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: w.Namespace, Name: w.Name, Labels: map[string]string{"app.kubernetes.io/managed-by": "toolchain-manager"}},
			Data:       data,
		}
		return w.Client.Create(ctx, &cm)
	}
	if err != nil {
		return err
	}
	if sameData(cm.Data, data) {
		return nil
	}
	cm.Data = data
	return w.Client.Update(ctx, &cm)
}

func sameData(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (w *Watch) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Watch) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}
