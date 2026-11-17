package server

import (
	"context"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	dexProxyChildEnv  = "TOOLCHAIN_MANAGER_DEX_PROXY_CHILD"
	dexProxyCAFileEnv = "TOOLCHAIN_MANAGER_DEX_PROXY_CA_FILE"
)

func TestGuardFollowsProxyEnvironment(t *testing.T) {
	backend := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(backend.Close)
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: backend.Certificate().Raw})
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		caFile     string
		noProxy    string
		wantTunnel bool
	}{
		{name: "issuer behind HTTPS_PROXY", wantTunnel: true},
		{name: "issuer with a private CA behind HTTPS_PROXY", caFile: caFile, wantTunnel: true},
		{name: "issuer with a private CA in NO_PROXY", caFile: caFile, noProxy: "example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newTunnelProxy(t, backend.Listener.Addr().String())

			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], //nolint:gosec // re-executes this test binary
				"-test.run=^TestGuardProxyChild$", "-test.count=1", "-test.v")
			command.Env = append(environmentWithoutProxy(),
				dexProxyChildEnv+"=1",
				dexProxyCAFileEnv+"="+tc.caFile,
				"HTTPS_PROXY="+proxy.server.URL,
				"NO_PROXY="+tc.noProxy,
			)
			output, _ := command.CombinedOutput()

			tunnels := proxy.connectedHosts()
			if !tc.wantTunnel {
				if len(tunnels) != 0 {
					t.Fatalf("tunnels = %v, want none; child output:\n%s", tunnels, output)
				}
				return
			}
			if len(tunnels) == 0 || tunnels[0] != "example.com:443" {
				t.Fatalf("tunnels = %v, want example.com:443 first; child output:\n%s", tunnels, output)
			}
		})
	}
}

// TestGuardProxyChild runs only as the child process of
// TestGuardFollowsProxyEnvironment: net/http reads the proxy variables once
// per process, so each proxy setting needs a fresh process.
func TestGuardProxyChild(t *testing.T) {
	if os.Getenv(dexProxyChildEnv) != "1" {
		t.Skip("child process of TestGuardFollowsProxyEnvironment")
	}
	_, err := NewGuard(t.Context(), AuthConfig{
		Issuer:    "https://example.com",
		CAFile:    os.Getenv(dexProxyCAFileEnv),
		Audiences: []string{"toolchain-manager"},
	}, slog.New(slog.DiscardHandler))
	t.Logf("NewGuard: %v", err)
}

func environmentWithoutProxy() []string {
	var environment []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasSuffix(strings.ToUpper(name), "_PROXY") {
			continue
		}
		environment = append(environment, entry)
	}
	return environment
}

// tunnelProxy is an HTTPS proxy that records each CONNECT target and tunnels
// every one of them to a fixed backend address.
type tunnelProxy struct {
	server  *httptest.Server
	mutex   sync.Mutex
	targets []string
}

func newTunnelProxy(t *testing.T, backendAddress string) *tunnelProxy {
	t.Helper()
	proxy := &tunnelProxy{}
	proxy.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		proxy.mutex.Lock()
		proxy.targets = append(proxy.targets, r.Host)
		proxy.mutex.Unlock()

		backendConnection, err := net.Dial("tcp", backendAddress)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			_ = backendConnection.Close()
			http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
			return
		}
		clientConnection, _, err := hijacker.Hijack()
		if err != nil {
			_ = backendConnection.Close()
			return
		}
		_, _ = clientConnection.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		go func() {
			_, _ = io.Copy(backendConnection, clientConnection)
			_ = backendConnection.Close()
		}()
		_, _ = io.Copy(clientConnection, backendConnection)
		_ = clientConnection.Close()
	}))
	t.Cleanup(proxy.server.Close)
	return proxy
}

func (proxy *tunnelProxy) connectedHosts() []string {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	return append([]string(nil), proxy.targets...)
}
