package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/identity"
)

// fakeDex is an OIDC issuer signing ID tokens with one RSA key.
type fakeDex struct {
	srv    *httptest.Server
	signer jose.Signer
}

func newFakeDex(t *testing.T) *fakeDex {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDex{signer: signer}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": d.srv.URL, "jwks_uri": d.srv.URL + "/keys", "authorization_endpoint": d.srv.URL + "/auth",
			"token_endpoint": d.srv.URL + "/token", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k", Algorithm: "RS256", Use: "sig"}}})
	})
	d.srv = httptest.NewServer(mux)
	t.Cleanup(d.srv.Close)
	return d
}

func (d *fakeDex) token(t *testing.T, aud string) string {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{
		"iss": d.srv.URL, "sub": "s1", "aud": aud, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"email": "admin@lab.local", "email_verified": true, "groups": []string{"platform-admins"},
	})
	jws, err := d.signer.Sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGuard(t *testing.T) {
	dex := newFakeDex(t)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"login":"octocat"}`))
	}))
	defer gh.Close()
	g, err := NewGuard(context.Background(), AuthConfig{
		Issuer: dex.srv.URL, Audiences: []string{"muster"}, GrantApp: "github", GitHub: &github.Client{API: gh.URL, HTTP: gh.Client()},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	type seen struct {
		caller, grant string
		header        bool
	}
	var got seen
	h := g.Protect(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		id, _ := identity.FromContext(r.Context())
		grant, _ := identity.Grant(r.Context())
		got = seen{caller: id.String(), grant: grant, header: r.Header.Get("X-Muster-Grant-github") != ""}
	}))

	for name, tc := range map[string]struct {
		bearer, grant string
		code          int
		want          seen
	}{
		"no bearer":        {code: http.StatusUnauthorized},
		"not a token":      {bearer: "nonsense", code: http.StatusUnauthorized},
		"foreign audience": {bearer: dex.token(t, "someone-else"), code: http.StatusUnauthorized},
		"no grant":         {bearer: dex.token(t, "muster"), code: http.StatusOK, want: seen{caller: "admin@lab.local"}},
		"verified grant":   {bearer: dex.token(t, "muster"), grant: "good", code: http.StatusOK, want: seen{caller: "admin@lab.local", grant: "good"}},
		"refused grant":    {bearer: dex.token(t, "muster"), grant: "revoked", code: http.StatusOK, want: seen{caller: "admin@lab.local"}},
	} {
		t.Run(name, func(t *testing.T) {
			got = seen{}
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.bearer != "" {
				r.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			if tc.grant != "" {
				r.Header.Set("X-Muster-Grant-github", tc.grant)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d: %s", w.Code, tc.code, w.Body)
			}
			if got != tc.want {
				t.Errorf("handler saw %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestGuardPinned(t *testing.T) {
	dex := newFakeDex(t)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"login":"octocat"}`))
	}))
	defer gh.Close()
	g, err := NewGuard(context.Background(), AuthConfig{
		Issuer: dex.srv.URL, Audiences: []string{"muster"}, Pinned: true, GitHub: &github.Client{API: gh.URL, HTTP: gh.Client()},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var caller, grant string
	var idHeader bool
	h := g.Protect(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		id, _ := identity.FromContext(r.Context())
		caller = id.String()
		grant, _ = identity.Grant(r.Context())
		idHeader = r.Header.Get(IDTokenHeader) != ""
	}))
	for name, tc := range map[string]struct {
		bearer, idToken string
		code            int
	}{
		"App token and Dex token": {bearer: "good", idToken: dex.token(t, "muster"), code: http.StatusOK},
		"no Dex token":            {bearer: "good", code: http.StatusUnauthorized},
		"no App token":            {idToken: dex.token(t, "muster"), code: http.StatusUnauthorized},
		"App token refused":       {bearer: "revoked", idToken: dex.token(t, "muster"), code: http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			caller, grant, idHeader = "", "", false
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.bearer != "" {
				r.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			if tc.idToken != "" {
				r.Header.Set(IDTokenHeader, tc.idToken)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d: %s", w.Code, tc.code, w.Body)
			}
			if tc.code == http.StatusOK && (caller != "admin@lab.local" || grant != "good" || idHeader) {
				t.Errorf("handler saw caller %q grant %q id header %v", caller, grant, idHeader)
			}
		})
	}
}
