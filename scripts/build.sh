#!/usr/bin/env bash
# Build static binaries for all three supported platforms.
# Phase 4 Step 2: cross-platform distribution.
#
# Usage:
#   ./scripts/build.sh                    # build all platforms
#   ./scripts/build.sh linux              # build only linux
#   ./scripts/build.sh windows darwin     # build windows and darwin
#
# Output: ./dist/forge-<goos>[-<arch>].exe (Windows) or
#         ./dist/forge-<goos>[-<arch>] (Linux, macOS).
#
# All binaries are statically linked (CGO_ENABLED=0) and stripped
# (ldflags=-s -w). The build is reproducible from any host — Go's
# cross-compile handles the OS/arch switch without a VM.

set -euo pipefail

# Resolve the project root regardless of where the script is
# invoked from. cd into it so go build ./cmd/forge resolves.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT}"

# Output directory. mkdir -p is idempotent.
DIST="${ROOT}/dist"
mkdir -p "${DIST}"

# Target platforms. Each entry is "GOOS [GOARCH]"; if GOARCH is
# omitted, the toolchain default for that GOOS is used (amd64 on
# most CI runners). The full matrix below matches what the
# .github/workflows/ci.yml produces.
ALL_TARGETS=(
  "windows amd64"
  "linux amd64"
  "darwin amd64"
  "darwin arm64"
)

# Filter to the requested set when the caller passes arguments.
TARGETS=()
if [ "$#" -eq 0 ]; then
  TARGETS=("${ALL_TARGETS[@]}")
else
  for want in "$@"; do
    found=0
    for t in "${ALL_TARGETS[@]}"; do
      read -r goos goarch <<<"${t}"
      if [ "${goos}" = "${want}" ]; then
        TARGETS+=("${t}")
        found=1
      fi
    done
    if [ "${found}" -eq 0 ]; then
      echo "build.sh: unknown target '${want}'" >&2
      echo "valid: windows linux darwin" >&2
      exit 64
    fi
  done
fi

# Build loop. We force CGO_ENABLED=0 so the binaries are fully
# static — no libgo, no libc, no DLLs — which matches the
# spec §0 "single static binary" requirement.
#
# -trimpath strips local filesystem paths from the binary so
# the output is reproducible across machines (no $GOPATH or
# /home/user/... baked into the symbol table). -s -w strips
# the symbol table and DWARF debug info to shrink the binary.
# No -X version override for now: core.AppVersion is a const
# and a proper version-injection step lands separately.
for t in "${TARGETS[@]}"; do
  read -r goos goarch <<<"${t}"
  ext=""
  if [ "${goos}" = "windows" ]; then
    ext=".exe"
  fi
  # Use a per-arch suffix only when the arch is non-default
  # for that GOOS (i.e. darwin/arm64), so the common amd64
  # artifacts keep the simple name.
  archsuffix=""
  if [ "${goarch}" != "amd64" ]; then
    archsuffix="-${goarch}"
  fi
  out="${DIST}/forge-${goos}${archsuffix}${ext}"
  echo ">>> GOOS=${goos} GOARCH=${goarch} -> ${out}"
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go build -trimpath \
      -ldflags="-s -w" \
      -o "${out}" ./cmd/forge
done

echo
echo "built ${#TARGETS[@]} binary(s) under ${DIST}"
ls -la "${DIST}"