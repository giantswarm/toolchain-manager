# The toolchain jq, written by the toolchain-manager from the packages
# and binaries picked for it: change it with add_packages and add_binary. The
# spec line below is what the manager reads back, the checksum line covers
# the rest of the file: a hand edit makes it a hand-written toolchain, which
# the manager leaves alone.
# toolchain-manager: {"name":"jq","source":"alpine","branch":"v3.24","base":"docker.io/library/alpine:3.24.3@sha256:b3413572228e34bd1c9245f5565e6cd127005655e8a7899aef33d8e83255c9e6","harnesses":["kagent","claude","codex","omp"],"packages":[{"name":"jq","version":"1.8.2-r0"}],"tools":[{"name":"jq","version":"jq --version"}]}
# toolchain-manager sha256: 0a6b2d282767360a6e4de78c2013869eec636b15c264de317e1d0bfd8aeab362

# The build stage installs everything at the mount directory /opt/kagent/tools/jq,
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

# Packages of the alpine source on v3.24, pinned.
RUN mkdir -p /opt/kagent/tools/jq \
 && apk add --no-cache --no-scripts --root /opt/kagent/tools/jq --initdb \
      --keys-dir /etc/apk/keys --repositories-file /etc/apk/repositories \
      musl \
      jq=1.8.2-r0 \
 && rm -f /opt/kagent/tools/jq/lib/apk/db/lock \
 && find /opt/kagent/tools/jq -type l | while read -r link; do \
      target=$(readlink "$link"); \
      case "$target" in /*) ln -sfn "/opt/kagent/tools/jq${target}" "$link" ;; esac; \
    done

# bin/: a wrapper per tool, its environment set.
RUN toolchain-wrap /opt/kagent/tools/jq jq ''

# The image is the mount directory: no user, no entrypoint.
FROM scratch
COPY --from=build /opt/kagent/tools/jq/ /
LABEL io.giantswarm.toolchain.name="jq" \
      io.giantswarm.toolchain.harnesses="kagent,claude,codex,omp" \
      io.giantswarm.toolchain.tool.jq="jq --version" \
      io.giantswarm.toolchain.package.jq="1.8.2-r0"
