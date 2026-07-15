#!/usr/bin/env bash
set -euo pipefail

tag="${1:?usage: package-release.sh <vX.Y.Z>}"
version="${tag#v}"
root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/dist/release"

rm -rf "$out"
mkdir -p "$out"

targets=(
  "linux amd64"
  "linux arm64"
  "darwin amd64"
  "darwin arm64"
  "windows amd64"
)

for target in "${targets[@]}"; do
  read -r goos goarch <<<"$target"
  name="blitterserver_${version}_${goos}_${goarch}"
  stage="$out/$name"
  binary="blitterserver"
  if [ "$goos" = "windows" ]; then
    binary="blitterserver.exe"
  fi

  mkdir -p "$stage"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath \
    -ldflags "-s -w -X main.version=$tag" \
    -o "$stage/$binary" ./cmd/blitterserver
  cp "$root/README.md" "$root/LICENSE" "$stage/"

  if [ "$goos" = "windows" ]; then
    (cd "$out" && zip -q -r "$name.zip" "$name")
  else
    tar -C "$out" -czf "$out/$name.tar.gz" "$name"
  fi
  rm -rf "$stage"
done

(cd "$out" && sha256sum ./*.tar.gz ./*.zip > SHA256SUMS)
