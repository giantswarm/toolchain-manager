package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostsWithTheServiceAccountToken(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sa-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["path"] = r.URL.Path
		got = append(got, body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"rv1","channel":"C0123ABCDE","ts":"1.0"}`))
	}))
	defer srv.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("sa-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Client{URL: srv.URL + "/", TokenFile: token, HTTP: srv.Client()}

	posted, err := c.Ask(context.Background(), Review{Team: "team-tools", Channel: "C0123ABCDE", Text: strings.Repeat("x", 4000),
		PullRequests: []string{"https://github.com/acme/tools/pull/7"}, Approve: Tool{Tool: "x_toolchain-manager_approve_update", Arguments: map[string]any{"pull": 7}}})
	if err != nil || posted.ID != "rv1" {
		t.Fatalf("ask = %+v, %v", posted, err)
	}
	if err := c.Notice(context.Background(), "team-tools", "C0123ABCDE", "outdated", "https://github.com/acme/tools/releases/tag/lint/v0.1.0"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["path"] != "/reviews" || got[1]["path"] != "/notices" ||
		len([]rune(got[0]["text"].(string))) != textMax || got[0]["approve"].(map[string]any)["tool"] != "x_toolchain-manager_approve_update" {
		t.Errorf("posted = %v", got)
	}
	if _, ok := got[1]["approve"]; ok {
		t.Errorf("a notice carries an approve: %v", got[1])
	}

	c.TokenFile = filepath.Join(t.TempDir(), "missing")
	if err := c.Notice(context.Background(), "t", "C0123ABCDE", "x", ""); err == nil || !strings.Contains(err.Error(), "ServiceAccount token") {
		t.Errorf("without a token = %v", err)
	}
}
