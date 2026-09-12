#!/usr/bin/env bash
set -Eeuo pipefail
script=$(cd "$(dirname "$0")" && pwd)/deploy.sh
scratch=$(mktemp -d -t server-monitor-deploy.XXXXXXXX)
cleanup() {
  case "$scratch" in */server-monitor-deploy.*) rm -rf -- "$scratch" ;; *) exit 2 ;; esac
}
trap cleanup EXIT
export MSYS=winsymlinks:sys
export MOCK_LOG="$scratch/commands" MOCK_STATE="$scratch/state" MOCK_SOURCE="$scratch/source"
export MOCK_SHA=1111111111111111111111111111111111111111
export MOCK_FAIL=none MOCK_RUNNING=yes
mkdir -p "$MOCK_SOURCE/web"
printf 'services: {}
' > "$MOCK_SOURCE/web/docker-compose.yml"
: > "$MOCK_LOG"
git() {
  [[ $1 = -C && $3 = archive && $4 = "$MOCK_SHA" ]] || return 2
  printf 'archive %s
' "$4" >> "$MOCK_LOG"
  command tar -C "$MOCK_SOURCE" -c web
}
docker() {
  printf '%s
' "$*" >> "$MOCK_LOG"
  case "$1" in
    ps) [[ $MOCK_RUNNING = no ]] || printf 'old-container
'; return 0 ;;
    inspect) printf 'sha256:previous-image
'; return 0 ;;
    image) return 0 ;;
    compose) ;;
    *) return 2 ;;
  esac
  case " $* " in
    *' config '*)
      [[ " $* " = *' --quiet '* ]] || printf 'services:
  backend:
    image: previous-pinned
'
      return 0 ;;
    *' pull '*) [[ $MOCK_FAIL != pull ]]; return ;;
    *' stop '*) printf stopped > "$MOCK_STATE"; return 0 ;;
    *' up '*)
      if [[ " $* " = *'/rollback.yml '* ]]; then
        [[ $MOCK_FAIL != rollback ]] || return 1
        printf previous > "$MOCK_STATE"
      else
        printf candidate > "$MOCK_STATE"
        [[ $MOCK_FAIL != up ]] || return 1
      fi
      return 0 ;;
  esac
  return 2
}
curl() {
  printf '%s
' "$*" >> "$MOCK_LOG"
  [[ $MOCK_FAIL != health ]] || return 22
  [[ $MOCK_FAIL != rollback ]] || return 22
  if [[ $MOCK_FAIL = page && $* = */files ]]; then printf broken; else printf '<div id="root"></div>'; fi
}
export -f git docker curl
if ! command -v flock >/dev/null; then
  printf 'Local platform lacks flock; lock behavior requires Linux CI
'
  flock() { [[ $1 = -w && $2 = 900 && $3 = 9 ]]; }
  export -f flock
fi
check() { if ! "$@"; then printf 'Assertion failed: %s
' "$*" >&2; exit 1; fi; }
new_case() {
  export DEPLOY_ROOT="$scratch/$1" MOCK_FAIL=none MOCK_RUNNING=yes
  mkdir -p "$DEPLOY_ROOT/web"
  printf 'ALLOW_REGISTRATION=false
TRUSTED_PROXIES=127.0.0.1
POLL_INTERVAL=17
JWT_SECRET=do-not-change
' > "$DEPLOY_ROOT/web/.env"
  printf 'services: {}
' > "$DEPLOY_ROOT/web/docker-compose.yml"
  printf previous > "$MOCK_STATE"
  : > "$MOCK_LOG"
}
deploy() { bash "$script" deploy "$MOCK_SHA" ghcr.io/example/server-monitor; }
new_case healthy
deploy
check cmp "$DEPLOY_ROOT/web/.env" "$DEPLOY_ROOT/shared/.env"
check test "$(readlink "$DEPLOY_ROOT/current")" = "$DEPLOY_ROOT/releases/$MOCK_SHA"
check test "$(cat "$MOCK_STATE")" = candidate
check grep -q "archive $MOCK_SHA" "$MOCK_LOG"
check grep -q "image tag sha256:previous-image server-monitor-rollback/backend:$MOCK_SHA" "$MOCK_LOG"
check grep -q "backend:$MOCK_SHA" "$DEPLOY_ROOT/current/images.env"
check test "$(wc -l < "$DEPLOY_ROOT/current/images.env")" -eq 2
lines=$(wc -l < "$MOCK_LOG")
deploy
check test "$(wc -l < "$MOCK_LOG")" -eq "$lines"
bash "$script" rollback "$MOCK_SHA"
check test "$(cat "$MOCK_STATE")" = previous
check test ! -e "$DEPLOY_ROOT/current"
export MOCK_SHA=2222222222222222222222222222222222222222
deploy
previous=$(readlink "$DEPLOY_ROOT/current")
printf 'ALLOW_REGISTRATION=false
CUSTOM_SETTING=retained
' >> "$DEPLOY_ROOT/shared/.env"
cp "$DEPLOY_ROOT/shared/.env" "$scratch/settings"
export MOCK_SHA=3333333333333333333333333333333333333333
deploy
check cmp "$scratch/settings" "$DEPLOY_ROOT/shared/.env"
check test "$(cat "$DEPLOY_ROOT/releases/$MOCK_SHA/previous")" = "$previous"
bash "$script" rollback "$MOCK_SHA"
check test "$(readlink "$DEPLOY_ROOT/current")" = "$previous"
lines=$(wc -l < "$MOCK_LOG")
bash "$script" rollback "$MOCK_SHA"
check test "$(wc -l < "$MOCK_LOG")" -eq "$lines"
for failure in up health page pull; do
  new_case "$failure"
  export MOCK_FAIL="$failure"
  if deploy; then echo 'Expected failed deployment'; exit 1; fi
  check test "$(cat "$MOCK_STATE")" = previous
  check test ! -e "$DEPLOY_ROOT/current"
  check cmp "$DEPLOY_ROOT/web/.env" "$DEPLOY_ROOT/shared/.env"
  export MOCK_FAIL=none
  deploy
  check test "$(cat "$MOCK_STATE")" = candidate
done
new_case rollback-failure
deploy
export MOCK_SHA=4444444444444444444444444444444444444444 MOCK_FAIL=rollback
previous=$(readlink "$DEPLOY_ROOT/current")
if deploy; then echo 'Expected rollback failure'; exit 1; fi
check test "$(readlink "$DEPLOY_ROOT/current")" = "$previous"
check test "$(cat "$MOCK_STATE")" = candidate
new_case first-release
export MOCK_RUNNING=no MOCK_FAIL=health
if deploy; then echo 'Expected first release failure'; exit 1; fi
check test "$(cat "$MOCK_STATE")" = stopped
check test ! -e "$DEPLOY_ROOT/current"
export MOCK_FAIL=none
deploy
check test "$(cat "$MOCK_STATE")" = candidate
new_case unconfigured
export DEPLOY_ROOT="$DEPLOY_ROOT/empty"
if deploy; then echo 'Expected missing configuration failure'; exit 1; fi
check test ! -e "$DEPLOY_ROOT/shared/.env"
if bash "$script" deploy main ghcr.io/example/server-monitor; then echo 'Expected unpinned commit rejection'; exit 1; fi
if type -P flock >/dev/null; then
  new_case serialized
  mkdir -p "$DEPLOY_ROOT/shared"
  exec 8>"$DEPLOY_ROOT/shared/deploy.lock"
  flock 8
  deploy >"$scratch/serialized-output" &
  pending=$!
  sleep 0.15
  check test ! -s "$MOCK_LOG"
  flock -u 8
  wait "$pending"
  exec 8>&-
  check test "$(cat "$MOCK_STATE")" = candidate
fi
printf 'Deployment protection tests passed\n'
