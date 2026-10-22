# example/toolchains

A toolchain source: toolchains for coding agents, each built by this
repository's CI into an image under `registry.example.com/example/toolchains`.

A **toolchain** is a named set of tools published as its own OCI image. A
coding agent's Session mounts it read-only at `/opt/kagent/tools/<name>`,
beside the unchanged image of the agent's Harness, with its `bin/` on `PATH`.

## Layout

| Path | What it is |
|---|---|
| `toolchains/<name>/Dockerfile` | A toolchain: image `registry.example.com/example/toolchains/<name>:X.Y.Z`, released as `<name>/vX.Y.Z` |
| `.github/workflows/toolchain-contract.yaml` | The toolchain contract check of every toolchain, required on every pull request |
| `.github/workflows/toolchain-release.yaml` | On a merge, each changed toolchain is checked, published under its next version and `latest`, and tagged |
| `renovate.json5` | Renovate keeps the workflows' pinned actions current; the toolchains' pins are the toolchain-manager's |

## Changing a toolchain

The toolchain-manager writes a toolchain's Dockerfile from the packages and
binaries picked for it (`add_packages`, `add_binary`); its first line names
it as written by the manager. A hand-written Dockerfile is the advanced mode:
the same contract check verifies it, and the manager no longer edits it.

The image root is the mount directory: everything is built at
`/opt/kagent/tools/<name>`, then copied to the root of a `scratch` image. Its
labels declare the toolchain: `io.giantswarm.toolchain.name` (the directory's
name), `io.giantswarm.toolchain.harnesses` (the Harnesses it serves) and one
`io.giantswarm.toolchain.tool.<tool>` per tool with its version command.

## The toolchain contract

Every pull request runs giantswarm/agent-toolchains' contract check against
each built toolchain: no user and no entrypoint, every file readable by uid
65532, built at its mount directory, every library under the prefix, and each
tool answering its version command beside every Harness image it declares.
A release publishes only a toolchain that keeps it.

The release needs the repository secrets `REGISTRY_USERNAME` and
`REGISTRY_PASSWORD` for `registry.example.com`.
