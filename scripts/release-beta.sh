#!/usr/bin/env bash

set -euo pipefail

if [[ -z "${VERSION:-}" ]]; then
  echo "VERSION is required (example: VERSION=0.3.0-beta.1)" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DIST_DIR="${DIST_DIR:-${REPO_ROOT}/dist}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

mkdir -p "${DIST_DIR}"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/cerberus-release.XXXXXX")"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

echo "Building Cerberus beta artifacts"
echo "  version: ${VERSION}"
echo "  build date: ${BUILD_DATE}"
echo "  dist dir: ${DIST_DIR}"

for arch in arm64 amd64; do
  work_dir="${tmp_dir}/${arch}"
  mkdir -p "${work_dir}"

  archive_path="${DIST_DIR}/cerberus_${VERSION}_darwin_${arch}.tar.gz"
  checksum_path="${archive_path}.sha256"

  echo
  echo "==> darwin/${arch}"

  (
    cd "${REPO_ROOT}"
    GOOS=darwin GOARCH="${arch}" go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.buildDate=${BUILD_DATE}" \
      -o "${work_dir}/cerberus" ./cmd/cerberus
  )

  tar -C "${work_dir}" -czf "${archive_path}" cerberus
  shasum -a 256 "${archive_path}" > "${checksum_path}"

  echo "  wrote: ${archive_path}"
  echo "  wrote: ${checksum_path}"
done

echo
echo "Done."
