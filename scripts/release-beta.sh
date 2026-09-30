#!/usr/bin/env bash

set -euo pipefail

if [[ -z "${VERSION:-}" ]]; then
  echo "VERSION is required (example: VERSION=0.5.0-beta.1)" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DIST_DIR="${DIST_DIR:-${REPO_ROOT}/dist}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
COMMIT="${COMMIT:-$(cd "${REPO_ROOT}" && git rev-parse --short HEAD 2>/dev/null || echo unknown)}"

# Targets to build. macOS-first per README, but linux artifacts are cheap
# (pure-Go deps) and let `go install` / tarball users on linux work.
TARGETS=(
  "darwin/arm64"
  "darwin/amd64"
  "linux/arm64"
  "linux/amd64"
)

# The presence helper (cmd/cerberus-presence) asks the person at the Mac
# through LocalAuthentication before a passkey enrollment. On darwin it is
# built with cgo, which needs this Mac's SDK: a darwin release is cut on a
# Mac. Elsewhere it is the refusing stub, built without cgo.
if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "the darwin archives carry cerberus-presence, built with cgo against the macOS SDK: cut the release on a Mac" >&2
  exit 1
fi

mkdir -p "${DIST_DIR}"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/cerberus-release.XXXXXX")"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

echo "Building Cerberus release artifacts"
echo "  version: ${VERSION}"
echo "  commit: ${COMMIT}"
echo "  build date: ${BUILD_DATE}"
echo "  dist dir: ${DIST_DIR}"

ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}"

declare -a archives

# build_presence builds cerberus-presence for os/arch into out. darwin: cgo
# against LocalAuthentication, the arch chosen by clang so one Mac builds
# both, and the result checked to link the framework, so a release can
# never ship the stub where the real helper belongs.
build_presence() {
  local os="$1" arch="$2" out="$3"
  if [[ "${os}" == "darwin" ]]; then
    local clang_arch="${arch}"
    [[ "${arch}" == "amd64" ]] && clang_arch="x86_64"
    (
      cd "${REPO_ROOT}"
      CGO_ENABLED=1 GOOS=darwin GOARCH="${arch}" CC="clang -arch ${clang_arch}" go build \
        -trimpath \
        -ldflags "-s -w" \
        -o "${out}" ./cmd/cerberus-presence
    )
    if ! otool -L "${out}" | grep -q LocalAuthentication.framework; then
      echo "cerberus-presence for darwin/${arch} does not link LocalAuthentication: it is the stub, not the helper" >&2
      exit 1
    fi
  else
    (
      cd "${REPO_ROOT}"
      CGO_ENABLED=0 GOOS="${os}" GOARCH="${arch}" go build \
        -trimpath \
        -ldflags "-s -w" \
        -o "${out}" ./cmd/cerberus-presence
    )
  fi
}

for target in "${TARGETS[@]}"; do
  os="${target%/*}"
  arch="${target#*/}"
  work_dir="${tmp_dir}/${os}_${arch}"
  mkdir -p "${work_dir}"

  archive_name="cerberus_${VERSION}_${os}_${arch}.tar.gz"
  archive_path="${DIST_DIR}/${archive_name}"
  checksum_path="${archive_path}.sha256"

  echo
  echo "==> ${os}/${arch}"

  (
    cd "${REPO_ROOT}"
    CGO_ENABLED=0 GOOS="${os}" GOARCH="${arch}" go build \
      -trimpath \
      -ldflags "${ldflags}" \
      -o "${work_dir}/cerberus" ./cmd/cerberus
  )

  build_presence "${os}" "${arch}" "${work_dir}/cerberus-presence"

  # Stage README + LICENSE alongside the binary, the conventional release layout.
  cp "${REPO_ROOT}/README.md" "${REPO_ROOT}/LICENSE" "${work_dir}/"

  tar -C "${work_dir}" -czf "${archive_path}" cerberus cerberus-presence README.md LICENSE
  shasum -a 256 "${archive_path}" > "${checksum_path}"

  echo "  wrote: ${archive_path}"
  echo "  wrote: ${checksum_path}"
  archives+=("${archive_name}")
done

# Emit a combined checksums.txt that scripts/render-homebrew-formula.sh and
# `gh release upload` can both consume. Entries are bare basenames so the file
# is usable from the dist/ directory without path rewriting.
combined_checksums="${DIST_DIR}/checksums.txt"
(
  cd "${DIST_DIR}"
  shasum -a 256 "${archives[@]}" > "${combined_checksums}"
)

echo
echo "Done."
echo "  combined checksums: ${combined_checksums}"
