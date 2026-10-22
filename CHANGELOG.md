# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).



## [Unreleased]

### Added

- `list_sources`, `add_source`, `approve_source` and `remove_source`: the installation's toolchain sources, the configured ones and those people add with the toolchain-manager's GitHub App grant and their Slack channel, each added one approved once by an installation owner; called as the person through muster, the forwarded Dex ID token validated and the person's App token read from muster's grant.
- The chart deploys the manager: Deployment, Service, the `ToolchainSource` CRD, its RBAC, the configured sources and the muster `MCPServer`.
- `list_toolchains` and `describe_toolchain`: the toolchains the offered sources have published (their `<name>/vX.Y.Z` GitHub Releases), each with its versions and image, and one version's Harnesses, tools, pins and egress as its Dockerfile labels the image; read from the sources with the person's App token.
- `create_toolchain`: opens a pull request, as the person, in an offered source that adds a toolchain from the Dockerfile `add_packages` and `add_binary` returned or a hand-written one, on the branch `toolchain/<name>`.
- `toolchain_build_status`: a toolchain's pull request from queued through its checks (checking, checked) and the merge's release (building) to published with its version and image, or failed with the failing job's log; `wait` waits up to 25 seconds for the state to move.

### Changed

- A new source's release workflow tags each toolchain's image `X.Y.Z`, without the `v` of its `<name>/vX.Y.Z` release, as giantswarm/agent-toolchains does.


[Unreleased]: https://github.com/giantswarm/toolchain-manager/tree/main
