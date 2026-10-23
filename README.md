# toolchain-manager

MCP server that manages toolchains for coding agents: the installation's toolchain sources, package search, binaries and the written Dockerfile.

A **toolchain** is a named set of tools (`go`, `node`) published as its own OCI image. A coding agent's Session mounts it read-only at `/opt/kagent/tools/<name>`, beside the unchanged image of the agent's Harness, with its `bin/` on `PATH`. A **toolchain source** is a git repository with a directory per toolchain whose CI checks, builds and publishes each one; [giantswarm/agent-toolchains](https://github.com/giantswarm/agent-toolchains) is the platform's own and holds the toolchain contract check every source runs.

## Tools

| Tool | What it does |
|---|---|
| `search_packages` | Searches a package source: each package's version per branch, and for an exact name its dependency closure (packages and shared libraries) per branch |
| `add_packages` | Adds packages to a toolchain, each pinned to its branch's version, and returns the Dockerfile |
| `add_binary` | Adds a release binary (a GitHub release asset or a URL), pinned by version and SHA-256 per architecture, and returns the Dockerfile |
| `init_source` | Renders a new toolchain source: layout, the contract check as a required CI workflow, the release workflow, optional Renovate, an example toolchain |
| `list_sources` | Lists the installation's toolchain sources, configured and added, with whether their toolchains are offered |
| `add_source` | Adds a GitHub repository the caller maintains, granted by the toolchain-manager's GitHub App, with the Slack channel that approves its updates |
| `approve_source` | Approves an added source, as the installation's owner, once |
| `remove_source` | Removes an added source, as the person who added it or an owner |
| `list_toolchains` | Lists the toolchains the installation offers, each with its published versions (newest first), every version's image, Harnesses and tools; agent-manager's `start_session` takes any version it lists and refuses any other |
| `describe_toolchain` | Describes a published toolchain at a version (the newest by default): its image, Harnesses, tools with their version commands, pinned packages and binaries, egress, and whether the manager wrote its Dockerfile |
| `create_toolchain` | Opens a pull request, as the person, in an offered source that adds a toolchain: the Dockerfile `add_packages` or `add_binary` returned, or a hand-written one, at `toolchains/<name>/Dockerfile` on the branch `toolchain/<name>` |
| `toolchain_build_status` | Follows a toolchain's pull request: queued, checking, checked, then after the merge building, published (version, image, release) or failed with the failing job's log |
| `deprecate_toolchain` | Takes a toolchain, or one version, out of the offered list, after naming the agents whose default toolchains name it and the Sessions that hold its digest; `restore` takes it back |

The package and Dockerfile tools return files and write to no repository; `create_toolchain` is the one that writes, as the person.

A published toolchain is a GitHub Release `<name>/vX.Y.Z` of an offered source, which its release workflow creates only for a toolchain that keeps the contract, its image `<registry>/<name>:X.Y.Z`. `list_toolchains` and `describe_toolchain` read the releases and the released Dockerfile (`toolchains/<name>/Dockerfile` at the tag), whose `io.giantswarm.toolchain.*` labels are the image's, with the person's App token: a person sees the toolchains of the sources the App's installation grants them, and a source it does not reach is listed as unreachable.

Beside `versions`, the names, each listed version is under `published` with the Harnesses and tools its released Dockerfile labels the image with, read once per release tag; a version whose Dockerfile cannot be read is listed with the reason. A Session names a toolchain with one of these versions, or none for the newest, and agent-manager's `start_session` asks `list_toolchains` as the person before it resolves the version to its digest, so a version the list does not carry, deprecated or never published, starts nothing.

`create_toolchain` commits the Dockerfile with the person's App token, so the pull request is theirs and needs their push access; a toolchain the source already has, or one with an open pull request, is refused. `toolchain_build_status` reads the pull request's check runs and commit statuses (GitHub Actions and CircleCI alike) and, once merged, the merge commit's, its `<name>/vX.Y.Z` tag, that tag's GitHub Release and the image `<registry>/<name>:X.Y.Z`, asked of the registry anonymously: published means the image is there and every job passed, since a source may release before its CI pushes (giantswarm/agent-toolchains' tag pipeline). For a registry that wants credentials, which the manager never holds, the jobs alone decide. Any failed job fails the build, its page the log. With `wait` it reads again every 5 seconds until the state moves, at most 25 seconds, inside muster's default call timeout.

## Sources and who calls

The tools are called as the person through muster: the chart's `MCPServer` (`muster.mcpServer.enabled`) has muster forward the person's Dex ID token as every call's bearer (`auth.forwardToken`), validated against Dex (`auth.dex.issuerURL`, `auth.trustedAudiences`), and attach the person's token of the toolchain-manager's GitHub App, whose grant muster keeps (`auth.providerApps`, `github.grantApp`), in `X-Muster-Grant-<app>`, verified with GitHub. Without `auth.dex.issuerURL` the source and toolchain tools are not served.

An installation's sources are the ones its configuration names (`sources`, approved by being named there) and the ones people add, each a `ToolchainSource` (`toolchain-manager.giantswarm.io/v1alpha1`) in the manager's namespace. `add_source` needs the App installed on the repository, which is the grant to read it, open pull requests and read its CI, checked with the caller's own App token (a user-to-server token reaches a repository only where the App is installed and the person has access), and push access for the caller. Without the caller's grant in muster it answers `grant_required` naming the App, which muster answers with its sign-in link; without the installation, `install_required` with `github.appInstallURL`. A source someone adds is `Pending` until an installation owner (`owners.groups`, `owners.emails`) approves it once with `approve_source`; an owner's own source is approved by adding it. An approved source's contract-checked toolchains are offered. The source's CI builds and publishes with its own credentials to its own registry.

## Deprecation

`deprecate_toolchain` takes a toolchain, or one of its versions, out of `list_toolchains`: a `ToolchainDeprecation` in the manager's namespace, by a person who can push to the toolchain's source or an installation owner. Without `confirm` it changes nothing and names who uses it: the agents whose default toolchains name it and the Sessions that hold its digest, which agent-manager's `toolchain_users` answers for the person through muster (`muster.mcpUrl`, `muster.agentManagerToolsPrefix`; without them the tool is not served). Its release and image stay: a Session started on it keeps its digest, and a suspended one resumes on it, since a snapshot restores only on the exact digest. With the newest version deprecated, a Session that names no version gets the newest one still offered. `list_toolchains` with `deprecated` shows them marked, and `describe_toolchain` describes a deprecated version; `restore` offers it again.

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
