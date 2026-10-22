# toolchain-manager

MCP server that manages toolchains for coding agents: sources, package search, binaries and the written Dockerfile.

A **toolchain** is a named set of tools (`go`, `node`) published as its own OCI image. A coding agent's Session mounts it read-only at `/opt/kagent/tools/<name>`, beside the unchanged image of the agent's Harness, with its `bin/` on `PATH`. A **toolchain source** is a git repository with a directory per toolchain whose CI checks, builds and publishes each one; [giantswarm/agent-toolchains](https://github.com/giantswarm/agent-toolchains) is the platform's own and holds the toolchain contract check every source runs.

## Tools

| Tool | What it does |
|---|---|
| `search_packages` | Searches a package source: each package's version per branch, and for an exact name its dependency closure (packages and shared libraries) per branch |
| `add_packages` | Adds packages to a toolchain, each pinned to its branch's version, and returns the Dockerfile |
| `add_binary` | Adds a release binary (a GitHub release asset or a URL), pinned by version and SHA-256 per architecture, and returns the Dockerfile |
| `init_source` | Renders a new toolchain source: layout, the contract check as a required CI workflow, the release workflow, optional Renovate, an example toolchain |

The tools return files and write to no repository.

## The written Dockerfile

From the picked packages and binaries the manager writes a toolchain's Dockerfile: a build stage that installs them under the mount directory (`--mount-root`, `/opt/kagent/tools` by default, plus the toolchain's name), since shebangs and library paths embed it, and a `scratch` image of that directory with the `io.giantswarm.toolchain.*` labels the contract check reads. Each declared tool gets a wrapper in `bin/` that sets its environment (`GOROOT`, `PYTHONHOME`) and runs it through the prefix's own musl loader and libraries, so it runs beside any Harness image. Alpine packages are installed with `apk add --root`; apk's lock is removed after it, since it is readable by root only and the contract check rejects a file uid 65532 cannot read.

The picks travel in the Dockerfile itself, on its `# toolchain-manager:` spec line, with a checksum line over the rest: pass the Dockerfile back to change the toolchain. A Dockerfile without them, or edited since, is hand-written, the advanced mode: the same contract check verifies it, and the manager does not edit it.

## Package sources

A package source implements `internal/packages.Source` (search with versions per branch, dependency closure, default tools, install step) and is registered by name; the tools take it as `source`. Alpine's (`internal/packages/alpine`) is the first: it reads each branch's `APKINDEX` from a mirror (`--alpine-mirror`).

## Development

```sh
make test
go test ./internal/api -update            # rewrite the golden files, then review the diff
go run ./hack/record-apkindex internal/packages/alpine/testdata/apkindex helm jq go python3 py3-pytest
scripts/check-golden-toolchains.sh <agent-toolchains checkout>   # build and contract-check the golden Dockerfiles
```

The `Toolchain contract` workflow builds every golden Dockerfile and runs the contract check on it, at the giantswarm/agent-toolchains commit the source template pins.
