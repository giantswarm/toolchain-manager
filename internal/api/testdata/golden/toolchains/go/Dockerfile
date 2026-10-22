# The toolchain go, written by the toolchain-manager from the packages
# and binaries picked for it: change it with add_packages and add_binary. The
# spec line below is what the manager reads back, the checksum line covers
# the rest of the file: a hand edit makes it a hand-written toolchain, which
# the manager leaves alone.
# toolchain-manager: {"name":"go","source":"alpine","branch":"v3.24","base":"docker.io/library/alpine:3.24.3@sha256:b3413572228e34bd1c9245f5565e6cd127005655e8a7899aef33d8e83255c9e6","harnesses":["kagent","claude","codex","omp"],"packages":[{"name":"go","version":"1.26.9-r0"}],"binaries":[{"name":"golangci-lint","version":"v2.15.0","from":"golangci/golangci-lint","assets":[{"arch":"amd64","url":"https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-amd64.tar.gz","sha256":"9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea"},{"arch":"arm64","url":"https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-arm64.tar.gz","sha256":"b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998"}]}],"tools":[{"name":"go","version":"go version","env":{"GOROOT":"{prefix}/usr/lib/go"}},{"name":"golangci-lint","path":"usr/local/bin/golangci-lint","version":"golangci-lint --version"}]}
# toolchain-manager sha256: 66637817aa7d1ade06a7a20924303264f18dc2fe2a1758c54c288892b39aa232

# The build stage installs everything at the mount directory /opt/kagent/tools/go,
# since shebangs and library paths embed it, and writes a wrapper per tool
# in bin/.
FROM docker.io/library/alpine:3.24.3@sha256:b3413572228e34bd1c9245f5565e6cd127005655e8a7899aef33d8e83255c9e6 AS build
ARG TARGETARCH
COPY --chmod=0755 <<'TOOLCHAIN_WRAP' /usr/local/bin/toolchain-wrap
#!/bin/sh
# toolchain-wrap <prefix> <tool> <path> [<VAR>=<value>...]
#
# Write <prefix>/bin/<tool>: the wrapper that sets the tool's environment
# and runs it through the prefix's own loader and libraries, so that it runs
# beside any Harness image. <path> is the command's file under the prefix;
# empty finds it in the prefix's usr/local/bin, usr/bin, bin, usr/sbin or
# sbin. A script runs through its interpreter the same way; a static binary
# is run as it is.
set -eu

prefix="$1" tool="$2" path="$3"
shift 3

target=""
if [ -n "$path" ]; then
  target="${prefix}/${path}"
else
  for d in usr/local/bin usr/bin bin usr/sbin sbin; do
    if [ -e "${prefix}/${d}/${tool}" ]; then
      target="${prefix}/${d}/${tool}"
      break
    fi
  done
fi
if [ -z "$target" ] || [ ! -e "$target" ]; then
  echo "toolchain-wrap: no command ${tool} under ${prefix}" >&2
  exit 1
fi
target=$(readlink -f "$target")
case "$target" in
"${prefix}"/*) ;;
*)
  echo "toolchain-wrap: ${tool} resolves to ${target}, outside ${prefix}" >&2
  exit 1
  ;;
esac
wrapper="${prefix}/bin/${tool}"
if [ "$target" = "$wrapper" ]; then
  echo "toolchain-wrap: ${tool} is installed as ${wrapper} itself, where its wrapper goes" >&2
  exit 1
fi

loader=""
for l in "${prefix}"/lib/ld-musl-*.so.1; do
  [ -e "$l" ] && loader="$l"
done
libs="${prefix}/lib:${prefix}/usr/lib"

# dynamic <file>: whether the file is a dynamically linked program whose
# libraries the prefix holds.
dynamic() {
  [ -n "$loader" ] && "$loader" --library-path "$libs" --list "$1" >/dev/null 2>&1
}

run="$target"
if dynamic "$target"; then
  run="${loader} --library-path ${libs} ${target}"
elif [ "$(head -c 2 "$target")" = "#!" ]; then
  # A script: its interpreter, when the prefix holds it, through the loader.
  # A package built for / names /usr/bin/python3; the prefix's is
  # <prefix>/usr/bin/python3.
  interpreter=$(head -n 1 "$target" | sed 's/^#![[:space:]]*//')
  first=${interpreter%% *}
  if [ "$first" = "/usr/bin/env" ]; then
    rest=${interpreter#* }
    for d in usr/local/bin usr/bin bin; do
      if [ -e "${prefix}/${d}/${rest%% *}" ]; then
        first="${prefix}/${d}/${rest%% *}"
        interpreter="${first}${rest#"${rest%% *}"}"
        break
      fi
    done
  fi
  case "$first" in
  "${prefix}"/*) ;;
  /*)
    if [ -e "${prefix}${first}" ]; then
      first="${prefix}${first}"
      interpreter="${prefix}${interpreter}"
    fi
    ;;
  esac
  case "$first" in
  "${prefix}"/*)
    if dynamic "$(readlink -f "$first")"; then
      run="${loader} --library-path ${libs} ${interpreter} ${target}"
    fi
    ;;
  esac
fi

mkdir -p "${prefix}/bin"
{
  echo '#!/bin/sh'
  for assignment in "$@"; do
    echo "export ${assignment%%=*}='${assignment#*=}'"
  done
  echo "exec ${run} \"\$@\""
} >"$wrapper"
chmod 0755 "$wrapper"
echo "toolchain-wrap: bin/${tool} runs ${run}"
TOOLCHAIN_WRAP
COPY --chmod=0755 <<'TOOLCHAIN_FETCH' /usr/local/bin/toolchain-fetch
#!/bin/sh
# toolchain-fetch <prefix> <name> <url> <sha256> [<path>]
#
# Download a release binary, verify its SHA-256 checksum and install it as
# <prefix>/usr/local/bin/<name>. An archive (.tar.gz, .tgz, .tar.xz, .zip)
# is unpacked and its file <path> installed, or its file called <name>.
set -eu

prefix="$1" name="$2" url="$3" sum="$4" path="${5:-}"

work=$(mktemp -d)
wget -q -O "${work}/download" "$url"
echo "${sum}  ${work}/download" | sha256sum -c -

mkdir -p "${work}/files"
case "$url" in
*.tar.gz | *.tgz) tar -xzf "${work}/download" -C "${work}/files" ;;
*.tar.xz) tar -xJf "${work}/download" -C "${work}/files" ;;
*.zip) unzip -q "${work}/download" -d "${work}/files" ;;
*)
  mv "${work}/download" "${work}/files/${name}"
  path="$name"
  ;;
esac

if [ -n "$path" ]; then
  file="${work}/files/${path}"
else
  file=$(find "${work}/files" -type f -name "$name" | head -n 1)
fi
if [ -z "$file" ] || [ ! -f "$file" ]; then
  echo "toolchain-fetch: no file ${path:-$name} in ${url}" >&2
  exit 1
fi
mkdir -p "${prefix}/usr/local/bin"
cp "$file" "${prefix}/usr/local/bin/${name}"
chmod 0755 "${prefix}/usr/local/bin/${name}"
rm -rf "$work"
echo "toolchain-fetch: usr/local/bin/${name} from ${url}"
TOOLCHAIN_FETCH

# Packages of the alpine source on v3.24, pinned.
RUN mkdir -p /opt/kagent/tools/go \
 && apk add --no-cache --no-scripts --root /opt/kagent/tools/go --initdb \
      --keys-dir /etc/apk/keys --repositories-file /etc/apk/repositories \
      musl \
      go=1.26.9-r0 \
 && rm -f /opt/kagent/tools/go/lib/apk/db/lock \
 && find /opt/kagent/tools/go -type l | while read -r link; do \
      target=$(readlink "$link"); \
      case "$target" in /*) ln -sfn "/opt/kagent/tools/go${target}" "$link" ;; esac; \
    done

# golangci-lint v2.15.0 from golangci/golangci-lint, pinned by its checksum.
RUN case "${TARGETARCH}" in \
      amd64) set -- 'https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-amd64.tar.gz' 9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea ;; \
      arm64) set -- 'https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-arm64.tar.gz' b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998 ;; \
      *) echo "golangci-lint has no asset for ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && toolchain-fetch /opt/kagent/tools/go golangci-lint "$1" "$2" ''

# bin/: a wrapper per tool, its environment set.
RUN toolchain-wrap /opt/kagent/tools/go go '' 'GOROOT=/opt/kagent/tools/go/usr/lib/go' \
 && toolchain-wrap /opt/kagent/tools/go golangci-lint usr/local/bin/golangci-lint

# The image is the mount directory: no user, no entrypoint.
FROM scratch
COPY --from=build /opt/kagent/tools/go/ /
LABEL io.giantswarm.toolchain.name="go" \
      io.giantswarm.toolchain.harnesses="kagent,claude,codex,omp" \
      io.giantswarm.toolchain.tool.go="go version" \
      io.giantswarm.toolchain.tool.golangci-lint="golangci-lint --version" \
      io.giantswarm.toolchain.package.go="1.26.9-r0" \
      io.giantswarm.toolchain.binary.golangci-lint="v2.15.0"
