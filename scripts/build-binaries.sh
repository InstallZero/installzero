#!/usr/bin/env bash
# Builds the installzero binaries for release into dist/bin, with SHA256SUMS.
# Usage: scripts/build-binaries.sh <version> [os/arch ...]
#
# Release flags: -trimpath and -s -w leave out build paths and debug symbols.
# Unsigned Go binaries with them are flagged far less often by antivirus
# machine-learning heuristics (Defender's Bearfoos.A!ml quarantined a plain
# build). -buildid= with a fixed version makes the same commit build the same
# bytes.
set -euo pipefail
version="${1:?usage: build-binaries.sh <version> [os/arch ...]}"
shift
targets=("$@")
if [ ${#targets[@]} -eq 0 ]; then
  targets=(windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)
fi

cd "$(dirname "$0")/.."
out=dist/bin
rm -rf "$out"
mkdir -p "$out"
for t in "${targets[@]}"; do
  os="${t%/*}" arch="${t#*/}"
  ext=""
  [ "$os" = windows ] && ext=".exe"
  name="installzero-$os-$arch$ext"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -buildid= -X github.com/InstallZero/installzero/internal/cli.Version=$version" \
    -o "$out/$name" ./cmd/installzero
  echo "built $name"
done
(cd "$out" && sha256sum installzero-* > SHA256SUMS)
