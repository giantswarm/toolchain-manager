# toolchain-manager

MCP server that manages toolchains for coding agents, their sources, package search and the Dockerfile they are built from.

**Homepage:** <https://github.com/giantswarm/toolchain-manager>

## Source Code

* <https://github.com/giantswarm/toolchain-manager>

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| image.name | string | `"giantswarm/toolchain-manager"` | Image repository below `registry.domain`. |
| image.tag | string | `""` | Image tag; empty is the chart's appVersion. |
| image.pullPolicy | string | `"IfNotPresent"` |  |
| registry.domain | string | `"gsoci.azurecr.io"` |  |
| replicaCount | int | `1` |  |
| mcp.path | string | `"/mcp"` | Path of the MCP endpoint. |
| service.port | int | `8080` |  |
| auth | object | `{"dex":{"caSecret":{"key":"ca.crt","name":""},"discoveryURL":"","issuerURL":""},"trustedAudiences":[]}` | How every MCP call is authenticated: muster forwards the person's Dex ID token as its bearer (the MCPServer's `auth.forwardToken`). With `auth.dex.issuerURL` set, every call needs a valid token and the source tools are served; empty serves the package and Dockerfile tools only, to anyone who reaches the Service. |
| auth.dex.issuerURL | string | `""` | Dex issuer URL, as the tokens name it. |
| auth.dex.discoveryURL | string | `""` | Where the manager reaches Dex when the issuer's public name does not resolve in the cluster; empty is the issuer. |
| auth.dex.caSecret | object | `{"key":"ca.crt","name":""}` | Secret (name, key) in the release namespace holding the PEM CA of a Dex with a private certificate; empty name trusts the system roots. |
| auth.trustedAudiences | list | `[]` | OAuth client ids whose Dex ID tokens are accepted: the platform client and the audiences muster requires. |
| owners | object | `{"emails":[],"groups":[]}` | The installation's owners, who approve a source a person adds once before its toolchains are offered: Dex groups and verified emails. Without any, an added source is never approved. |
| sources | list | `[{"registry":"gsoci.azurecr.io/giantswarm/agent-toolchains","repository":"giantswarm/agent-toolchains"}]` | The toolchain sources the installation's configuration names, approved by being named here: each a GitHub repository (owner/name), the registry prefix its toolchains publish under, and the Slack channel of the team that approves its updates. |
| github.api | string | `"https://api.github.com"` | GitHub API. |
| github.grantApp | string | `"github"` | Provider App muster keeps the person's grant of the toolchain-manager's GitHub App as: muster attaches the person's token in `X-Muster-Grant-<app>` (an `aggregator.providerApps` entry of muster). |
| github.appInstallURL | string | `""` | Where a repository's owner installs the App, https://github.com/apps/<slug>/installations/new; add_source names it. |
| crds.install | bool | `true` | Install the ToolchainSource CRD; kept on uninstall, so removing the chart never removes an added source. |
| muster.mcpServer.enabled | bool | `false` | Register this server with muster: an `MCPServer` in the release namespace, its tools `x_<name>_<tool>`. Needs `auth.dex.issuerURL`. |
| muster.mcpServer.name | string | `"toolchain-manager"` | MCPServer name, the tools' prefix. |
| muster.mcpServer.displayName | string | `"Toolchain Manager"` | Name the Dev Portal shows for this server. |
| muster.mcpServer.autoStart | bool | `true` |  |
| muster.mcpServer.description | string | `"Toolchains for coding agents: sources, package search, binaries and the Dockerfile they are built from"` |  |
| muster.mcpServer.timeout | int | `0` | Tool call timeout in seconds; 0 keeps muster's default. |
| muster.mcpServer.requiredAudiences | list | `[]` | Audiences muster requests at login and forwards in the token. |
| muster.mcpServer.labels | object | `{}` | Labels added to (and overriding) the MCPServer's. |
| resources.requests.cpu | string | `"20m"` |  |
| resources.requests.memory | string | `"64Mi"` |  |
| resources.limits.memory | string | `"256Mi"` |  |
| podSecurityContext.runAsNonRoot | bool | `true` |  |
| podSecurityContext.runAsUser | int | `65532` |  |
| podSecurityContext.runAsGroup | int | `65532` |  |
| podSecurityContext.seccompProfile.type | string | `"RuntimeDefault"` |  |
| securityContext.allowPrivilegeEscalation | bool | `false` |  |
| securityContext.readOnlyRootFilesystem | bool | `true` |  |
| securityContext.capabilities.drop[0] | string | `"ALL"` |  |

## CI values

`ci/ci-values.yaml` holds the values the CI render (`helm template` in app-build-suite) needs beyond the defaults, such as a `required` value without a default.
