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

	"github.com/giantswarm/toolchain-manager/internal/api"
	"github.com/giantswarm/toolchain-manager/internal/binaries"
	"github.com/giantswarm/toolchain-manager/internal/packages"
	"github.com/giantswarm/toolchain-manager/internal/packages/alpine"
	"github.com/giantswarm/toolchain-manager/internal/toolchain"
)

type serveOptions struct {
	listen       string
	mcpPath      string
	alpineMirror string
	githubAPI    string
	mountRoot    string
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
	return c
}

func serve(ctx context.Context, o serveOptions) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: 2 * time.Minute}
	sources, err := packages.NewRegistry(alpine.New(alpine.Config{Fetcher: alpine.HTTPFetcher{Mirror: o.alpineMirror, Client: client}}))
	if err != nil {
		return err
	}
	svc := &api.Service{
		Sources:       sources,
		Writer:        toolchain.Writer{Sources: sources, MountRoot: o.mountRoot},
		Binaries:      binaries.Resolver{API: o.githubAPI, Token: os.Getenv("GITHUB_TOKEN"), Client: client},
		DefaultSource: alpine.Name,
	}

	mux := http.NewServeMux()
	mux.Handle(o.mcpPath, mcpserver.NewStreamableHTTPServer(api.NewMCPServer(svc, version), mcpserver.WithEndpointPath(o.mcpPath)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, "ok") })
	srv := &http.Server{Addr: o.listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("serving", "listen", o.listen, "mcp", o.mcpPath, "version", version)
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
