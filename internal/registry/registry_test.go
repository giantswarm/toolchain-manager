package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// TestExistsFollowsTheAnonymousTokenFlow serves acme/jq:0.2.0 behind an
// anonymous token, and private/* to no one.
func TestExistsFollowsTheAnonymousTokenFlow(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") == "repository:acme/jq:pull" && r.URL.Query().Get("x") == "1" {
				_, _ = w.Write([]byte(`{"access_token":"anon"}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		case r.Header.Get("Authorization") != "Bearer anon":
			scope := strings.TrimPrefix(strings.Split(r.URL.Path, "/manifests/")[0], "/v2/")
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token?x=1",service="reg",scope="repository:`+scope+`:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.Method == http.MethodHead && r.URL.Path == "/v2/acme/jq/manifests/0.2.0" && strings.Contains(r.Header.Get("Accept"), "oci.image.index"):
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	c := &Client{HTTP: srv.Client(), Scheme: "http"}

	for image, want := range map[string]bool{host + "/acme/jq:0.2.0": true, host + "/acme/jq:0.3.0": false} {
		got, err := c.Exists(context.Background(), image)
		if err != nil || got != want {
			t.Errorf("%s = %v, %v, want %v", image, got, err, want)
		}
	}
	if _, err := c.Exists(context.Background(), host+"/private/jq:0.2.0"); !errors.Is(err, ErrUnknown) {
		t.Errorf("a registry wanting credentials = %v, want ErrUnknown", err)
	}
	if _, err := c.Exists(context.Background(), "jq"); err == nil {
		t.Error("an image without host and tag was asked")
	}
}

func TestTheDefaultClientReachesPublicAddressesOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	c := &Client{Scheme: "http"}
	if _, err := c.Exists(context.Background(), strings.TrimPrefix(srv.URL, "http://")+"/acme/jq:0.2.0"); err == nil || !strings.Contains(err.Error(), "not public") {
		t.Errorf("a loopback registry = %v, want refused", err)
	}
	for addr, want := range map[string]bool{"20.50.2.1": true, "10.0.0.1": false, "169.254.169.254": false, "::1": false, "::ffff:127.0.0.1": false, "fd00::1": false} {
		if got := public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("public(%s) = %v, want %v", addr, got, want)
		}
	}
}
