package binaries

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestResolveRefuses(t *testing.T) {
	r := Resolver{API: "https://api.github.invalid"}
	for name, req := range map[string]Request{
		"a path in the repository":  {Name: "x", Repository: "owner/../../user", Version: "v1", Asset: "x"},
		"a path in the version":     {Name: "x", Repository: "owner/repo", Version: "v1/../../x", Asset: "x"},
		"a plain-HTTP URL":          {Name: "x", URL: "http://example.com/x", Version: "1.0"},
		"a command name with a /":   {Name: "../x", URL: "https://example.com/x", Version: "1.0"},
		"both a repository and URL": {Name: "x", Repository: "owner/repo", URL: "https://example.com/x", Version: "v1", Asset: "x"},
	} {
		if _, err := r.Resolve(context.Background(), req); err == nil {
			t.Errorf("%s: resolved", name)
		}
	}
}

func TestPublicClientRefusesALoopbackAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) }))
	defer srv.Close()
	resp, err := PublicClient(5 * time.Second).Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("reached a loopback address")
	}
	if !strings.Contains(err.Error(), "not a public address") {
		t.Errorf("error %v, want the refusal", err)
	}
}

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"140.82.121.4": true, "2606:50c0:8000::153": true,
		"10.1.2.3": false, "100.64.0.1": false, "127.0.0.1": false, "169.254.169.254": false,
		"192.168.1.1": false, "198.18.0.1": false, "0.0.0.0": false, "240.0.0.1": false,
		"::1": false, "::ffff:10.0.0.1": false, "::ffff:127.0.0.1": false, "fd00::1": false,
		"fe80::1": false, "64:ff9b::a00:1": false, "2002:a00:1::": false,
	} {
		if got := public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("public(%s) = %v, want %v", addr, got, want)
		}
	}
}
