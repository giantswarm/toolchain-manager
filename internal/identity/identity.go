// Package identity carries the authenticated caller through a request: who
// the OAuth layer validated (subject, email, groups) and the person's GitHub
// App token muster attached to the call. The toolchain-manager's source tools
// act for a caller only, so a context without an identity exists in tests
// and in a server that runs without OAuth.
package identity

import "context"

// Identity is the authenticated caller.
type Identity struct {
	// Subject is the IdP's stable user id (the `sub` claim).
	Subject string
	// Email is the caller's email when the IdP provided one.
	Email string
	// EmailVerified is the IdP's word that Email is the caller's.
	EmailVerified bool
	// Groups are the IdP group claims (Dex `groups`).
	Groups []string
}

// String is the caller as recorded and reported: the verified email, else
// the subject. An email the IdP does not vouch for names nobody.
func (id *Identity) String() string {
	if id == nil {
		return ""
	}
	if id.Email != "" && id.EmailVerified {
		return id.Email
	}
	return id.Subject
}

type (
	identityKey struct{}
	grantKey    struct{}
)

// ContextWith returns ctx carrying id.
func ContextWith(ctx context.Context, id *Identity) context.Context {
	if id == nil {
		return ctx
	}
	return context.WithValue(ctx, identityKey{}, id)
}

// FromContext returns the caller, if any.
func FromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(*Identity)
	return id, ok && id != nil
}

// ContextWithGrant returns ctx carrying the person's GitHub App token, as
// muster attached it to the call.
func ContextWithGrant(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, grantKey{}, token)
}

// Grant returns the person's GitHub App token on this call, if muster
// attached one.
func Grant(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(grantKey{}).(string)
	return t, ok && t != ""
}
