#!/usr/bin/env bash
set -euo pipefail

mapfile -t tags < <(git tag --list 'v1.*' --sort=-version:refname)
base_tag="${tags[0]:-}"
if [ -z "$base_tag" ]; then
  echo "No published v1 tag found; skipping API compatibility comparison."
  exit 0
fi

echo "Checking api/openapi.yaml compatibility with $base_tag..."
go run github.com/oasdiff/oasdiff@v1.23.0 breaking \
  "$base_tag:api/openapi.yaml" api/openapi.yaml \
  --fail-on ERR --format text
