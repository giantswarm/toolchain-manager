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
| auth | object | `{"dex":{"caSecret":{"key":"ca.crt","name":""},"issuerURL":"","jwksURL":""},"trustedAudiences":[]}` | How every MCP call is authenticated: muster forwards the person's Dex ID token as its bearer (the MCPServer's `auth.forwardToken`). With `auth.dex.issuerURL` set, every call needs a valid token and the source tools are served; empty serves the package and Dockerfile tools only, to anyone who reaches the Service. |
| auth.dex.issuerURL | string | `""` | Dex issuer URL, as the tokens name it. |
| auth.dex.jwksURL | string | `""` | Where Dex's signing keys are fetched when the issuer's public name does not resolve in the cluster; empty: discovered from the issuer. |
| auth.dex.caSecret | object | `{"key":"ca.crt","name":""}` | Secret (name, key) in the release namespace holding the PEM CA of a Dex with a private certificate; empty name trusts the system roots. |
| auth.trustedAudiences | list | `[]` | OAuth client ids whose Dex ID tokens are accepted: the platform client and the audiences muster requires. |
| owners | object | `{"emails":[],"groups":[]}` | The installation's owners, who approve a source a person adds once before its toolchains are offered: Dex groups and verified emails. Without any, an added source is never approved. |
| sources | list | `[{"registry":"gsoci.azurecr.io/giantswarm/agent-toolchains","repository":"giantswarm/agent-toolchains"}]` | The toolchain sources the installation's configuration names, approved by being named here: each a GitHub repository (owner/name), the registry prefix its toolchains publish under, and the owning team: its GitHub team slug (`team`) and its Slack channel's ID (`slackChannel`, C0123ABCDE), where the outdated notices and a major update's ask go. |
| github.api | string | `"https://api.github.com"` | GitHub API. |
| github.grantApp | string | `"github"` | Provider App muster keeps the person's grant of the toolchain-manager's GitHub App as: muster attaches the person's token in `X-Muster-Grant-<app>` (an `aggregator.providerApps` entry of muster). |
| github.appInstallURL | string | `""` | Where a repository's owner installs the App, https://github.com/apps/<slug>/installations/new; add_source names it. |
| updates.interval | string | `"6h"` | How often the background job checks the pins of the toolchains the manager wrote and marks the outdated ones (Go duration); 0 serves no `update_toolchain`, `approve_update` and no outdated mark. |
| klausGateway | object | `{"audience":"klaus-gateway","url":""}` | klaus-gateway's team-review endpoint, where the outdated notices and a major update's ask with its Approve button are posted to a source's team channel. The manager calls it with its own projected ServiceAccount token, which klaus-gateway admits when its `reviews.allowedCallers` lists `system:serviceaccount:<namespace>:toolchain-manager`. Empty `url` posts nothing: the mark stays in the tools, and a major update waits for an approving review on GitHub. |
| klausGateway.url | string | `""` | Base URL, such as http://klaus-gateway.klaus-gateway.svc.cluster.local:8080. |
| klausGateway.audience | string | `"klaus-gateway"` | Audience of the projected token, klaus-gateway's `reviews.audience`. |
| crds.install | bool | `true` | Install the ToolchainSource CRD; kept on uninstall, so removing the chart never removes an added source. |
| muster.mcpUrl | string | `"http://muster.agent-platform.svc.cluster.local:8090/mcp"` | muster's MCP endpoint, where `deprecate_toolchain` asks agent-manager, as the person, which agents and Sessions use a toolchain. Rendered with `agentManagerToolsPrefix`; either empty serves no `deprecate_toolchain`. |
| muster.agentManagerToolsPrefix | string | `"x_agent-manager_"` | agent-manager's tool prefix in muster. |
| muster.mcpServer.enabled | bool | `false` | Register this server with muster: an `MCPServer` in the release namespace, its tools `x_<name>_<tool>`. Needs `auth.dex.issuerURL`. |
| muster.mcpServer.name | string | `"toolchain-manager"` | MCPServer name, the tools' prefix. |
| muster.mcpServer.displayName | string | `"Toolchain Manager"` | Name the Dev Portal shows for this server. |
| muster.mcpServer.autoStart | bool | `true` |  |
| muster.mcpServer.description | string | `"Toolchains for coding agents: sources, package search, binaries and the Dockerfile they are built from"` |  |
| muster.mcpServer.timeout | int | `0` | Tool call timeout in seconds; 0 keeps muster's default. |
| muster.mcpServer.requiredAudiences | list | `[]` | Audiences muster requests at login and forwards in the token. |
| muster.mcpServer.authorizationServer | object | `{}` | Pin the registration to the toolchain-manager's GitHub App instead of `github.grantApp`'s provider App (the shape before muster kept provider Apps): issuer (https://github.com/apps/<id>), expectedIssuer, authorizationEndpoint, tokenEndpoint and clientCredentialsSecretRef (name, namespace, clientIdKey, clientSecretKey) of muster's `auth.authorizationServer`, `grantScope: subject`. The bearer is then the person's App token, the Dex ID token arrives in `X-Muster-Id-Token`, and a person without the grant is answered with the App's sign-in link. Empty: provider Apps. |
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
