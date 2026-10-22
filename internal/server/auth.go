// Package server guards the MCP endpoint the way muster calls it: every
// call carries the person's Dex ID token as its bearer (MCPServer
// auth.forwardToken), validated against Dex's keys and the trusted
// audiences, and the person's token of the toolchain-manager's GitHub App
// muster keeps their grant of (auth.providerApps) in GrantHeader, verified
// with GitHub. The tools then act for the person on the request's context;
// a call without a valid Dex token is refused with 401.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/identity"
)

// GrantHeaderPrefix starts the header muster carries the person's token of
// one provider App in: X-Muster-Grant-<app>.
const GrantHeaderPrefix = "X-Muster-Grant-" //nolint:gosec // G101: a header name, not a credential

// grantCacheTTL is how long a verified App token is trusted without asking
// GitHub again: a revoked one must stop working soon.
const grantCacheTTL = 5 * time.Minute

// AuthConfig configures the guard.
type AuthConfig struct {
	// Issuer is Dex's issuer URL, which the tokens name.
	Issuer string
	// DiscoveryURL, when set, is where Dex is reached from the manager
	// instead of Issuer: an in-cluster address of an issuer whose public
	// name does not resolve there.
	DiscoveryURL string
	// CAFile is a PEM CA bundle of a Dex with a private certificate.
	CAFile string
	// Audiences are the OAuth client ids whose ID tokens are accepted: the
	// platform client and the audiences muster requires.
	Audiences []string
	// GrantApp is the provider App whose token muster attaches; empty reads
	// none.
	GrantApp string
	// GitHub verifies a grant token.
	GitHub *github.Client
}

// Guard validates the caller of every MCP call.
type Guard struct {
	verifier *oidc.IDTokenVerifier
	cfg      AuthConfig
	log      *slog.Logger

	mu    sync.Mutex
	cache map[[sha256.Size]byte]time.Time
}

// NewGuard discovers Dex and returns the guard.
func NewGuard(ctx context.Context, cfg AuthConfig, log *slog.Logger) (*Guard, error) {
	if cfg.Issuer == "" || len(cfg.Audiences) == 0 {
		return nil, errors.New("auth: the Dex issuer and at least one trusted audience are required")
	}
	hc := &http.Client{Timeout: 15 * time.Second}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile) // #nosec G304 -- operator-provided path
		if err != nil {
			return nil, fmt.Errorf("auth: read CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("auth: no CA certificate in %s", cfg.CAFile)
		}
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	ctx = oidc.ClientContext(ctx, hc)
	discovery := cfg.Issuer
	if cfg.DiscoveryURL != "" {
		ctx = oidc.InsecureIssuerURLContext(ctx, cfg.Issuer)
		discovery = cfg.DiscoveryURL
	}
	p, err := oidc.NewProvider(ctx, discovery)
	if err != nil {
		return nil, fmt.Errorf("auth: discover Dex at %s: %w", discovery, err)
	}
	keys := p.VerifierContext(oidc.ClientContext(context.Background(), hc), &oidc.Config{SkipClientIDCheck: true})
	log.Info("callers authenticated by their forwarded Dex ID token", "issuer", cfg.Issuer, "audiences", cfg.Audiences, "grantApp", cfg.GrantApp)
	return &Guard{verifier: keys, cfg: cfg, log: log, cache: map[[sha256.Size]byte]time.Time{}}, nil
}

// Protect admits a call with a valid Dex ID token and puts the caller and
// the verified App token on its context. Every grant header is dropped, so
// nothing downstream reads one unverified.
func (g *Guard) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := g.authenticate(ctx, strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
		if err != nil {
			g.log.Warn("call refused", "path", r.URL.Path, "error", err)
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
		ctx = identity.ContextWith(ctx, id)
		if g.cfg.GrantApp != "" {
			if token := strings.TrimSpace(r.Header.Get(GrantHeaderPrefix + g.cfg.GrantApp)); token != "" {
				switch err := g.verifyGrant(ctx, token); {
				case errors.Is(err, github.ErrRefused):
					g.log.Info("GitHub refused the person's App token: treated as not granted", "caller", id.String())
				case err != nil:
					g.log.Warn("App token not verified: kept for the tool's own call to GitHub", "error", err)
					ctx = identity.ContextWithGrant(ctx, token)
				default:
					ctx = identity.ContextWithGrant(ctx, token)
				}
			}
		}
		r = r.Clone(ctx)
		for name := range r.Header {
			if strings.HasPrefix(http.CanonicalHeaderKey(name), http.CanonicalHeaderKey(GrantHeaderPrefix)) {
				r.Header.Del(name)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Guard) authenticate(ctx context.Context, bearer string) (*identity.Identity, error) {
	if bearer == "" {
		return nil, errors.New("no bearer token: muster forwards the person's Dex ID token")
	}
	tok, err := g.verifier.Verify(ctx, bearer)
	if err != nil {
		return nil, fmt.Errorf("the bearer is no valid Dex ID token: %w", err)
	}
	if !slices.ContainsFunc(tok.Audience, func(a string) bool { return slices.Contains(g.cfg.Audiences, a) }) {
		return nil, fmt.Errorf("token audience %v matches none of the trusted audiences %v", tok.Audience, g.cfg.Audiences)
	}
	var claims struct {
		Email         string   `json:"email"`
		EmailVerified bool     `json:"email_verified"`
		Groups        []string `json:"groups"`
	}
	if err := tok.Claims(&claims); err != nil {
		return nil, fmt.Errorf("read the token's claims: %w", err)
	}
	return &identity.Identity{Subject: tok.Subject, Email: claims.Email, EmailVerified: claims.EmailVerified, Groups: claims.Groups}, nil
}

// verifyGrant asks GitHub whose token this is, unless a recent answer
// already trusts it.
func (g *Guard) verifyGrant(ctx context.Context, token string) error {
	key := sha256.Sum256([]byte(token))
	now := time.Now()
	g.mu.Lock()
	until, ok := g.cache[key]
	g.mu.Unlock()
	if ok && now.Before(until) {
		return nil
	}
	if _, err := g.cfg.GitHub.Login(ctx, token); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, until := range g.cache {
		if !now.Before(until) {
			delete(g.cache, k)
		}
	}
	g.cache[key] = now.Add(grantCacheTTL)
	return nil
}
