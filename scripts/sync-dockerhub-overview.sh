#!/usr/bin/env bash
set -euo pipefail

username="${DOCKER_USERNAME:-matjam}"
repository="${DOCKER_REPOSITORY:-blitterserver}"
readme="${1:-README.md}"

: "${DOCKER_TOKEN:?DOCKER_TOKEN is required}"
test -f "$readme"

readme_bytes="$(wc -c <"$readme")"
if [ "$readme_bytes" -gt 25000 ]; then
  echo "Docker Hub overview exceeds the 25,000-byte limit: $readme_bytes bytes" >&2
  exit 1
fi

auth_payload="$(jq -n \
  --arg identifier "$username" \
  --arg secret "$DOCKER_TOKEN" \
  '{identifier: $identifier, secret: $secret}')"
access_token="$(curl --fail --silent --show-error \
  --request POST \
  --header 'Content-Type: application/json' \
  --data "$auth_payload" \
  'https://hub.docker.com/v2/auth/token' | jq -er '.access_token')"

overview_payload="$(jq -Rs '{full_description: .}' <"$readme")"
response="$(curl --fail --silent --show-error \
  --request PATCH \
  --header 'Content-Type: application/json' \
  --header "Authorization: Bearer $access_token" \
  --data "$overview_payload" \
  "https://hub.docker.com/v2/repositories/$username/$repository")"

remote_overview="$(jq -er '.full_description' <<<"$response")"
local_overview="$(<"$readme")"
if [ "$remote_overview" != "$local_overview" ]; then
  echo "Docker Hub returned an overview that differs from $readme" >&2
  exit 1
fi

echo "Updated Docker Hub overview for $username/$repository from $readme"
