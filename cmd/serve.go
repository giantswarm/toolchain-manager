package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/yaml"

	"github.com/giantswarm/toolchain-manager/api/v1alpha1"
	"github.com/giantswarm/toolchain-manager/internal/agentmanager"
	"github.com/giantswarm/toolchain-manager/internal/api"
	"github.com/giantswarm/toolchain-manager/internal/binaries"
	"github.com/giantswarm/toolchain-manager/internal/gateway"
	"github.com/giantswarm/toolchain-manager/internal/github"
	"github.com/giantswarm/toolchain-manager/internal/packages"
	"github.com/giantswarm/toolchain-manager/internal/packages/alpine"
	"github.com/giantswarm/toolchain-manager/internal/registry"
	"github.com/giantswarm/toolchain-manager/internal/server"
	"github.com/giantswarm/toolchain-manager/internal/sources"
	"github.com/giantswarm/toolchain-manager/internal/toolchain"
	"github.com/giantswarm/toolchain-manager/internal/toolchains"
	"github.com/giantswarm/toolchain-manager/internal/updates"
)

type serveOptions struct {
	listen       string
	mcpPath      string
	alpineMirror string
	githubAPI    string
	mountRoot    string

	dexIssuer        string
	dexJWKSURL       string
	dexCAFile        string
	trustedAudiences []string
	namespace        string
	sourcesFile      string
	ownerGroups      []string
	ownerEmails      []string
	grantApp         string
	grantPinned      bool
	appInstallURL    string
	musterMCPURL     string
	agentManagerTool string

	updatesInterval  time.Duration
	updatesConfigMap string
	gatewayURL       string
	gatewayTokenFile string
}

func newServeCommand() *cobra.Command {
	o := serveOptions{}
	c := &cobra.Command{
		Use:   "serve",
		Short: "Serve the MCP tools over streamable HTTP",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context(), o)
		},
	}
	c.Flags().StringVar(&o.listen, "listen", ":8080", "address to listen on")
	c.Flags().StringVar(&o.mcpPath, "mcp-path", "/mcp", "path of the MCP endpoint")
	c.Flags().StringVar(&o.alpineMirror, "alpine-mirror", "https://dl-cdn.alpinelinux.org/alpine", "Alpine mirror the package indexes are read from")
	c.Flags().StringVar(&o.githubAPI, "github-api", "https://api.github.com", "GitHub API the release binaries are resolved through; GITHUB_TOKEN authenticates, when set")
	c.Flags().StringVar(&o.mountRoot, "mount-root", toolchain.DefaultMountRoot, "directory a Session mounts its toolchains under, each at <mount-root>/<name>")
	c.Flags().StringVar(&o.dexIssuer, "dex-issuer-url", os.Getenv("DEX_ISSUER_URL"), "Dex issuer whose ID tokens muster forwards as every call's bearer; set, every call is authenticated and the source tools are served (DEX_ISSUER_URL)")
	c.Flags().StringVar(&o.dexJWKSURL, "dex-jwks-url", "", "where Dex's signing keys are fetched when the issuer's public name does not resolve in the cluster; default: discovered from the issuer")
	c.Flags().StringVar(&o.dexCAFile, "dex-ca-file", "", "PEM CA bundle of a Dex with a private certificate")
	c.Flags().StringSliceVar(&o.trustedAudiences, "trusted-audiences", nil, "OAuth client ids whose Dex ID tokens are accepted: the platform client and the audiences muster requires")
	c.Flags().StringVar(&o.namespace, "namespace", os.Getenv("POD_NAMESPACE"), "namespace of the added sources (ToolchainSources), the manager's own (POD_NAMESPACE)")
	c.Flags().StringVar(&o.sourcesFile, "sources-file", "", "YAML file of the sources the installation's configuration names: {sources: [{repository, registry, slackChannel}]}")
	c.Flags().StringSliceVar(&o.ownerGroups, "owner-groups", nil, "Dex groups of the installation's owners, who approve a new source")
	c.Flags().StringSliceVar(&o.ownerEmails, "owner-emails", nil, "verified emails of the installation's owners, who approve a new source")
	c.Flags().StringVar(&o.grantApp, "grant-app", "github", "provider App muster keeps the person's grant of the toolchain-manager's GitHub App as, attached in "+server.GrantHeaderPrefix+"<app> (the MCPServer's auth.providerApps)")
	c.Flags().BoolVar(&o.grantPinned, "grant-pinned", false, "the MCPServer is pinned to the toolchain-manager's GitHub App (auth.authorizationServer, grantScope subject, forwardIdentity): the bearer is the person's App token and the Dex ID token arrives in "+server.IDTokenHeader)
	c.Flags().StringVar(&o.appInstallURL, "github-app-install-url", "", "where a repository's owner installs the toolchain-manager's GitHub App, https://github.com/apps/<slug>/installations/new")
	c.Flags().StringVar(&o.musterMCPURL, "muster-mcp-url", os.Getenv("MUSTER_MCP_URL"), "muster's MCP endpoint (http(s)://host[:port]/mcp), where deprecate_toolchain asks agent-manager, as the person, who uses a toolchain. Needs --agent-manager-tools-prefix (MUSTER_MCP_URL)")
	c.Flags().StringVar(&o.agentManagerTool, "agent-manager-tools-prefix", os.Getenv("AGENT_MANAGER_TOOLS_PREFIX"), "agent-manager's tool prefix in muster (x_agent-manager_). Empty: deprecate_toolchain is not served (AGENT_MANAGER_TOOLS_PREFIX)")
	c.Flags().DurationVar(&o.updatesInterval, "updates-interval", 6*time.Hour, "how often the background job checks the pins of the toolchains the manager wrote; 0 serves no updates")
	c.Flags().StringVar(&o.updatesConfigMap, "updates-configmap", "toolchain-manager-updates", "ConfigMap in --namespace the background job keeps the toolchains' pin state in")
	c.Flags().StringVar(&o.gatewayURL, "klaus-gateway-url", "", "klaus-gateway's base URL, where the outdated notices are posted to the source's team channel (POST /notices). Needs --klaus-gateway-token-file; empty posts nothing")
	c.Flags().StringVar(&o.gatewayTokenFile, "klaus-gateway-token-file", "/var/run/secrets/klaus-gateway/token", "the projected ServiceAccount token for klaus-gateway's audience")
	return c
}

func serve(ctx context.Context, o serveOptions) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: 2 * time.Minute}
	pkgSources, err := packages.NewRegistry(alpine.New(alpine.Config{Fetcher: alpine.HTTPFetcher{Mirror: o.alpineMirror, Client: client}}))
	if err != nil {
		return err
	}
	svc := &api.Service{
		Sources:       pkgSources,
		Writer:        toolchain.Writer{Sources: pkgSources, MountRoot: o.mountRoot},
		Binaries:      binaries.Resolver{API: o.githubAPI, Token: os.Getenv("GITHUB_TOKEN"), Client: binaries.PublicClient(2 * time.Minute)},
		DefaultSource: alpine.Name,
	}

	var guard *server.Guard
	if o.dexIssuer != "" {
		gh := &github.Client{API: o.githubAPI, HTTP: binaries.PublicClient(20 * time.Second)}
		kube, err := o.kube()
		if err != nil {
			return err
		}
		if svc.ToolchainSources, err = o.sources(gh, kube); err != nil {
			return err
		}
		svc.Toolchains = &toolchains.Catalog{Sources: svc.ToolchainSources, GitHub: gh, Registry: &registry.Client{}}
		if o.updatesInterval > 0 {
			o.updates(ctx, svc, gh, kube)
		}
		if (o.musterMCPURL == "") != (o.agentManagerTool == "") {
			return errors.New("--muster-mcp-url and --agent-manager-tools-prefix go together: agent-manager is called through muster under its prefix")
		}
		if o.musterMCPURL != "" {
			users, err := agentmanager.New(o.musterMCPURL, o.agentManagerTool, version)
			if err != nil {
				return err
			}
			svc.Toolchains.Users = users
		}
		if guard, err = server.NewGuard(ctx, server.AuthConfig{
			Issuer: o.dexIssuer, JWKSURL: o.dexJWKSURL, CAFile: o.dexCAFile, Audiences: o.trustedAudiences, GrantApp: o.grantApp, Pinned: o.grantPinned, GitHub: gh,
		}, slog.Default()); err != nil {
			return err
		}
	}
	var mcpHandler http.Handler = mcpserver.NewStreamableHTTPServer(api.NewMCPServer(svc, version), mcpserver.WithEndpointPath(o.mcpPath))
	if guard != nil {
		mcpHandler = guard.Protect(mcpHandler)
	}

	mux := http.NewServeMux()
	mux.Handle(o.mcpPath, http.MaxBytesHandler(mcpHandler, 1<<20))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, "ok") })
	srv := &http.Server{Addr: o.listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("serving", "listen", o.listen, "mcp", o.mcpPath, "version", version, "sources", svc.ToolchainSources != nil, "toolchains", svc.Toolchains != nil, "deprecation", svc.Toolchains != nil && svc.Toolchains.Users != nil,
		"updates", svc.Toolchains != nil && svc.Toolchains.Updater != nil, "slack", o.gatewayURL != "")
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// sources builds the source keeper: the configured sources, the owners and
// the API client of the added ones.
func (o serveOptions) sources(gh *github.Client, kube client.Client) (*sources.Manager, error) {
	if o.namespace == "" {
		return nil, errors.New("--namespace (POD_NAMESPACE) names where the added sources are kept")
	}
	m := &sources.Manager{
		Namespace: o.namespace, Owners: sources.Owners{Groups: o.ownerGroups, Emails: o.ownerEmails},
		GitHub: gh, GrantApp: o.grantApp, InstallURL: o.appInstallURL,
	}
	if o.sourcesFile != "" {
		raw, err := os.ReadFile(o.sourcesFile)
		if err != nil {
			return nil, fmt.Errorf("read the configured sources: %w", err)
		}
		var file struct {
			Sources []sources.Configured `json:"sources"`
		}
		if err := yaml.UnmarshalStrict(raw, &file); err != nil {
			return nil, fmt.Errorf("read the configured sources %s: %w", o.sourcesFile, err)
		}
		m.Configured = file.Sources
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if len(m.Owners.Groups) == 0 && len(m.Owners.Emails) == 0 {
		slog.Warn("no installation owner is configured (--owner-groups, --owner-emails): an added source is never approved")
	}
	m.Client = kube
	return m, nil
}

// kube is the manager's Kubernetes client: its ToolchainSources,
// ToolchainDeprecations and the updates' ConfigMap.
func (o serveOptions) kube() (client.Client, error) {
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	return c, nil
}

// updates serves update_toolchain and starts the background job that marks
// outdated toolchains, posting into the sources' team channels through
// klaus-gateway when it is named.
func (o serveOptions) updates(ctx context.Context, svc *api.Service, gh *github.Client, kube client.Client) {
	updater := &updates.Updater{Packages: svc.Sources, Writer: svc.Writer, Binaries: svc.Binaries, GitHub: gh, Token: os.Getenv("GITHUB_TOKEN")}
	watch := &updates.Watch{Updater: updater, Client: kube, Namespace: o.namespace, Name: o.updatesConfigMap, Interval: o.updatesInterval}
	svc.Toolchains.Updater, svc.Toolchains.Updates = updater, watch
	if o.gatewayURL != "" {
		watch.Notifier = &gateway.Client{URL: o.gatewayURL, TokenFile: o.gatewayTokenFile}
	}
	go watch.Run(ctx)
}
