#!/usr/bin/env bash
set -euo pipefail

: "${ACTIONS_ID_TOKEN_REQUEST_URL:?GitHub Actions OIDC requires id-token: write}"
: "${ACTIONS_ID_TOKEN_REQUEST_TOKEN:?GitHub Actions OIDC requires id-token: write}"

# Obtain a fresh assertion on every invocation. Never cache it or print it in a
# workflow step: the provider captures stdout and passes it to the token exchange.
curl --fail --silent --show-error --connect-timeout 5 --max-time 20 \
  --header "Authorization: Bearer ${ACTIONS_ID_TOKEN_REQUEST_TOKEN}" \
  "${ACTIONS_ID_TOKEN_REQUEST_URL}&audience=https%3A%2F%2Fapi.anthropic.com" \
  | jq --exit-status --raw-output '.value | select(type == "string" and length > 0)'
