#!/usr/bin/env bash
# Run only in a disposable Linux container. curl/systemctl are local mocks.
set -euo pipefail
[[ ${AGENT_INSTALL_TEST_CONTAINER:-} == 1 ]] || { echo 'This test requires a disposable container' >&2; exit 1; }
installer="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/install.sh"
test_dir=$(mktemp -d)
export AGENT_TEST_ROOT=$test_dir
mkdir "$test_dir/bin"
mkdir -p /etc/systemd/system /usr/local/bin
cat > "$test_dir/source" <<'AGENT'
#!/bin/sh
set -eu
action=$1; shift
config=; force=false
while [ "$#" -gt 0 ]; do
 case "$1" in
  --config) config=$2; shift 2;;
  --server-url) shift 2;;
  --ticket-stdin) shift;;
  --force) force=true; shift;;
  *) exit 2;;
 esac
done
case "$action" in
 version) echo version-one;;
 config-url) echo https://panel.invalid;;
 check) test -f "$config";;
 enroll)
  IFS= read -r ticket
  [ "$ticket" != invalid ] || exit 1
  [ ! -f "$config" ] || $force
  printf '%s\n' "$ticket" > "$config"
  # Model the panel's irreversible token rotation as part of registration.
  printf '%s\n' "$ticket" > "$AGENT_TEST_ROOT/registered.identity"
  chmod 600 "$config"
  ;;
 *) exit 2;;
esac
AGENT
cat > "$test_dir/bin/curl" <<'CURL'
#!/bin/sh
set -eu
output=; checksum=false
while [ "$#" -gt 0 ]; do
 case "$1" in
  -o) output=$2; shift 2;;
  *'?checksum=1') checksum=true; shift;;
  *) shift;;
 esac
done
if $checksum; then sha256sum "$AGENT_TEST_ROOT/source" > "$output"; else cp "$AGENT_TEST_ROOT/source" "$output"; fi
CURL
cat > "$test_dir/bin/systemctl" <<'SYSTEMCTL'
#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$AGENT_TEST_ROOT/systemctl.log"
case "$1" in
 show) echo root;;
 is-enabled) test -f "$AGENT_TEST_ROOT/enabled";;
 is-active) test -f "$AGENT_TEST_ROOT/active";;
 enable) touch "$AGENT_TEST_ROOT/enabled";;
 disable)
  rm -f "$AGENT_TEST_ROOT/enabled"
  case " $* " in *' --now '*) rm -f "$AGENT_TEST_ROOT/active";; esac
  ;;
 stop) rm -f "$AGENT_TEST_ROOT/active";;
 restart)
  version=$(/usr/local/bin/server-monitor-agent version)
  printf 'restart-version %s\n' "$version" >> "$AGENT_TEST_ROOT/systemctl.log"
  if [ -f "$AGENT_TEST_ROOT/reject_version" ] && [ "$version" = "$(cat "$AGENT_TEST_ROOT/reject_version")" ]; then
    rm -f "$AGENT_TEST_ROOT/active"
    exit 1
  fi
  cmp -s /etc/server-monitor-agent/config.json "$AGENT_TEST_ROOT/registered.identity" || exit 1
  touch "$AGENT_TEST_ROOT/active"
  ;;
esac
exit 0
SYSTEMCTL
chmod +x "$test_dir/bin/"* "$test_dir/source"
export PATH="$test_dir/bin:$PATH"
bash "$installer" install --server-url https://panel.invalid --ticket initial
test "$(cat /etc/server-monitor-agent/config.json)" = initial
test "$(stat -c '%a' /etc/server-monitor-agent/config.json)" = 600
bash "$installer" upgrade
test "$(cat /etc/server-monitor-agent/config.json)" = initial
bash "$installer" reenroll --server-url https://panel.invalid --ticket replacement
test "$(cat /etc/server-monitor-agent/config.json)" = replacement

# Make both old unit and new binary distinguishable so rollback cannot pass
# merely because all mock versions happen to contain the same bytes.
printf '\n# operator-customized unit\n' >> /etc/systemd/system/server-monitor-agent.service
cp -p /usr/local/bin/server-monitor-agent "$test_dir/expected.binary"
cp -p /etc/systemd/system/server-monitor-agent.service "$test_dir/expected.unit"
cp -p /etc/server-monitor-agent/config.json "$test_dir/expected.config"
sed 's/version-one/version-broken/' "$test_dir/source" > "$test_dir/source.next"
mv "$test_dir/source.next" "$test_dir/source"
echo version-broken > "$test_dir/reject_version"
for action in upgrade reenroll; do
  if bash "$installer" "$action" --server-url https://panel.invalid --ticket retry; then echo 'expected restart failure' >&2; exit 1; fi
  cmp /usr/local/bin/server-monitor-agent "$test_dir/expected.binary"
  cmp /etc/systemd/system/server-monitor-agent.service "$test_dir/expected.unit"
  if [[ $action == reenroll ]]; then
    test "$(cat /etc/server-monitor-agent/config.json)" = retry
  else
    cmp /etc/server-monitor-agent/config.json "$test_dir/expected.config"
  fi
  cmp /etc/server-monitor-agent/config.json "$test_dir/registered.identity"
  test -f "$test_dir/active"
  test -f "$test_dir/enabled"
  test "$(tail -n 1 "$test_dir/systemctl.log")" = 'restart-version version-one'
done
bash "$installer" uninstall
test -f /etc/server-monitor-agent/config.json
test ! -e /usr/local/bin/server-monitor-agent
bash "$installer" uninstall --purge-config
test ! -e /etc/server-monitor-agent/config.json
if bash "$installer" install --server-url https://panel.invalid --ticket first-failure; then echo 'expected first-install failure' >&2; exit 1; fi
test ! -e /usr/local/bin/server-monitor-agent
test ! -e /etc/systemd/system/server-monitor-agent.service
test "$(cat /etc/server-monitor-agent/config.json)" = first-failure
cmp /etc/server-monitor-agent/config.json "$test_dir/registered.identity"
test ! -e "$test_dir/active"
test ! -e "$test_dir/enabled"
test ! -e /usr/local/bin/server-monitor-agent.new
bash "$installer" uninstall --purge-config
test ! -e /etc/server-monitor-agent/config.json
echo 'install, upgrade rollback, reenrollment identity retention, first-install cleanup, uninstall retention and purge passed'
