# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).



## [Unreleased]

### Added

- `list_sources`, `add_source`, `approve_source` and `remove_source`: the installation's toolchain sources, the configured ones and those people add with the toolchain-manager's GitHub App grant and their Slack channel, each added one approved once by an installation owner; called as the person through muster, the forwarded Dex ID token validated and the person's App token read from muster's grant.
- The chart deploys the manager: Deployment, Service, the `ToolchainSource` CRD, its RBAC, the configured sources and the muster `MCPServer`.


[Unreleased]: https://github.com/giantswarm/toolchain-manager/tree/main
