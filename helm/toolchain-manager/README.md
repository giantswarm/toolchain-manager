# toolchain-manager

Please add description

**Homepage:** <https://github.com/giantswarm/toolchain-manager>

## Source Code

* <https://github.com/some-org/some-repo>

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| image.name | string | `"giantswarm/toolchain-manager"` |  |
| image.tag | string | `""` |  |
| registry.domain | string | `"gsoci.azurecr.io"` |  |

## CI values

`ci/ci-values.yaml` holds the values the CI render (`helm template` in app-build-suite) needs beyond the defaults, such as a `required` value without a default.
