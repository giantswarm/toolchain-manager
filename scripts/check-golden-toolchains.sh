#!/usr/bin/env bash
set -euo pipefail

# Build every Dockerfile the toolchain-manager's golden files hold and run
# giantswarm/agent-toolchains' toolchain contract check on it beside each
# Harness image it declares.
#
# Usage: scripts/check-golden-toolchains.sh <agent-toolchains checkout> [<dir>...]
#   (default: every golden toolchain and init_source's example)

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <agent-toolchains checkout> [<dir>...]" >&2
  exit 2
fi
contract="$1"
shift
root="$(cd "$(dirname "$0")/.." && pwd)"
golden="${root}/internal/api/testdata/golden"

if [[ $# -gt 0 ]]; then
  dirs=("$@")
else
  mapfile -t dirs < <(find "${golden}/toolchains" "${golden}/init/toolchains" -mindepth 2 -maxdepth 2 -name Dockerfile -printf '%h\n' | sort)
fi

failed=0
for dir in "${dirs[@]}"; do
  name=$(basename "$dir")
  image="toolchain-manager-golden/${name}:check"
  echo "::group::${dir#"${root}"/}"
  if ! docker build -t "$image" "$dir"; then
    echo "FAIL  ${dir#"${root}"/}: the build failed"
    failed=1
  elif ! "${contract}/scripts/toolchain-contract.sh" "$image" "${contract}/harnesses"; then
    failed=1
  fi
  echo "::endgroup::"
done

exit "$failed"
