#!/usr/bin/env bash
set -euo pipefail

base_url="${BASE_URL:-http://127.0.0.1:8080}"
output_dir="${OUTPUT_DIR:-artifacts/ui-snapshots}"
playwright_cli_version="${PLAYWRIGHT_CLI_VERSION:-0.1.17}"
session="klipbord-ui-snapshots"

# playwright-cli keeps its daemon session outside the npm cache. Keep it in a
# temporary directory so local runs do not require access to a user-wide cache.
export PWTEST_DAEMON_SESSION_DIR="${PWTEST_DAEMON_SESSION_DIR:-${TMPDIR:-/tmp}/klipbord-playwright-daemon}"

mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"

pwcli() {
  local config_args=()
  if [ "$1" = "open" ] && [ -n "${PLAYWRIGHT_CLI_CONFIG:-}" ]; then
    config_args=("--config=$PLAYWRIGHT_CLI_CONFIG")
  fi

  npx --yes --package="@playwright/cli@${playwright_cli_version}" \
    playwright-cli --session "$session" "$@" "${config_args[@]}"
}

cleanup() {
  pwcli close >/dev/null 2>&1 || true
}
trap cleanup EXIT

wait_for_server() {
  for _ in $(seq 1 30); do
    if curl --fail --silent --show-error "$base_url/health" >/dev/null; then
      return 0
    fi
    sleep 1
  done

  echo "Klipbord did not become ready at $base_url" >&2
  return 1
}

wait_for_server

text_item="$(curl --fail --silent --show-error \
  --header 'Content-Type: application/json' \
  --data '{"content":"Visual regression seed text","name":"ui-seed.txt","ttl":"7d"}' \
  "$base_url/api/text")"
text_id="$(printf '%s' "$text_item" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')"

if [ -z "$text_id" ]; then
  echo "Could not read the seeded text item ID" >&2
  exit 1
fi

curl --fail --silent --show-error \
  --form 'file=@internal/app/testdata/sample_screenshot.png;type=image/png' \
  --form 'ttl=7d' \
  "$base_url/api/upload" >/dev/null

pwcli open "$base_url/clip"
pwcli resize 1440 1000
pwcli snapshot >"$output_dir/clipboard-desktop.snapshot.txt"
pwcli run-code "async page => { await page.locator('[data-item-id=\"$text_id\"]').waitFor(); }"
pwcli run-code "async page => { await page.screenshot({ path: '$output_dir/clipboard-desktop.png', fullPage: true }); }"

curl --fail --silent --show-error \
  --request PATCH \
  --header 'Content-Type: application/json' \
  --data '{"persistent":true}' \
  "$base_url/api/files/$text_id" >/dev/null

pwcli run-code "async page => { await page.getByRole('link', { name: 'Persistent', exact: true }).click(); }"
pwcli snapshot >"$output_dir/persistent-desktop.snapshot.txt"
pwcli run-code "async page => { await page.locator('#persistentGrid [data-item-id=\"$text_id\"]').waitFor(); }"
pwcli run-code "async page => { await page.locator('#persistentGrid [data-preview-id=\"$text_id\"] code').waitFor(); }"
pwcli run-code "async page => { await page.screenshot({ path: '$output_dir/persistent-desktop.png', fullPage: true }); }"

pwcli reload
pwcli run-code "async page => { if (page.url() !== '$base_url/persist') throw new Error('refresh did not keep /persist'); await page.locator('#tab-persistent.active').waitFor(); }"

pwcli run-code "async page => { await page.getByRole('link', { name: 'Clipboard', exact: true }).click(); }"
pwcli resize 390 844
pwcli snapshot >"$output_dir/clipboard-mobile.snapshot.txt"
pwcli run-code "async page => { await page.screenshot({ path: '$output_dir/clipboard-mobile.png', fullPage: true }); }"
