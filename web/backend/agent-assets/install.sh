#!/usr/bin/env bash
# Linux installer. Run a downloaded copy; no host is modified by the build.
set -euo pipefail
umask 077

service=server-monitor-agent
binary=/usr/local/bin/server-monitor-agent
config_dir=/etc/server-monitor-agent
config="$config_dir/config.json"
unit=/etc/systemd/system/server-monitor-agent.service
action=${1:-install}
if (($#)); then shift; fi
server_url=
ticket=
ticket_stdin=false
run_user=root
purge=false
while (($#)); do
  case "$1" in
    --server-url) server_url=${2:?missing panel URL}; shift 2 ;;
    --ticket) ticket=${2:?missing enrollment ticket}; shift 2 ;;
    --ticket-stdin) ticket_stdin=true; shift ;;
    --user) run_user=${2:?missing service user}; shift 2 ;;
    --purge-config) purge=true; shift ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done
[[ $(uname -s) == Linux ]] || { echo 'Linux is required' >&2; exit 1; }
[[ $EUID == 0 ]] || { echo 'Run this installer as root' >&2; exit 1; }
command -v systemctl >/dev/null || { echo 'systemd is required' >&2; exit 1; }

if [[ $action == uninstall ]]; then
  systemctl disable --now "$service.service" 2>/dev/null || true
  rm -f -- "$unit" "$binary"
  systemctl daemon-reload
  if $purge; then
    # Fixed, explicitly scoped files; never recursively delete a supplied path.
    rm -f -- "$config"
    rmdir -- "$config_dir" 2>/dev/null || true
  fi
  echo "Agent removed. Revoke its token in the panel; configuration is preserved unless --purge-config was used."
  exit 0
fi
[[ $action == install || $action == reenroll || $action == upgrade ]] || { echo 'Usage: agent-install.sh install|reenroll|upgrade|uninstall [options]' >&2; exit 2; }
if [[ $action == upgrade ]]; then
  [[ -f $config && -x $binary ]] || { echo 'No existing installation to upgrade' >&2; exit 1; }
  [[ -n $server_url ]] || server_url=$("$binary" config-url --config "$config")
fi
if [[ $action == reenroll && -f $config && -x $binary ]]; then
  [[ -n $server_url ]] || server_url=$("$binary" config-url --config "$config")
  previous_user=$(systemctl show "$service.service" -p User --value 2>/dev/null || true)
  [[ -z $previous_user ]] || run_user=$previous_user
fi
[[ -n $server_url ]] || { echo '--server-url is required' >&2; exit 2; }
server_url=${server_url%/}
case "$server_url" in
  https://*) protocols='=https' ;;
  http://127.0.0.1:*|http://localhost:*|http://\[::1\]:*) protocols='=http' ;;
  *) echo 'Use an HTTPS panel URL' >&2; exit 2 ;;
esac
[[ $run_user =~ ^[a-zA-Z_][a-zA-Z0-9_-]*\$?$ ]] || { echo 'Invalid service user' >&2; exit 2; }
id "$run_user" >/dev/null
case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo 'Only Linux amd64 and arm64 are supported' >&2; exit 1 ;;
esac
command -v curl >/dev/null
command -v sha256sum >/dev/null
scratch=$(mktemp -d)
completed=false
changing=false
had_binary=false
had_unit=false
had_config=false
enrolled=false
was_enabled=false
was_active=false
restore_file() {
  local target=$1 previous=$2 existed=$3
  if $existed; then
    cp -p -- "$previous" "$target.rollback" && mv -f -- "$target.rollback" "$target"
  else
    rm -f -- "$target"
  fi
}
cleanup() {
  local status=$?
  trap - EXIT
  set +e
  local restored=true
  if ! $completed && $changing; then
    # Stop the replacement before putting the previous files back. A first
    # failed install must not leave systemd retrying a broken executable.
    systemctl stop "$service.service" 2>/dev/null || true
    if ! $was_enabled; then systemctl disable "$service.service" 2>/dev/null || true; fi
    restore_file "$binary" "$scratch/binary.previous" "$had_binary" || restored=false
    restore_file "$unit" "$scratch/unit.previous" "$had_unit" || restored=false
    # A successful registration has already revoked the old token on the
    # panel. Rolling that identity back locally would strand the old binary.
    if ! $enrolled; then restore_file "$config" "$scratch/config.previous" "$had_config" || restored=false; fi
    systemctl daemon-reload || restored=false
    if $was_enabled; then systemctl enable "$service.service" || restored=false; fi
    if $was_active && $had_binary && $had_unit; then
      systemctl restart "$service.service" || echo 'Previous files restored, but the old service could not be restarted.' >&2
    fi
    if $restored; then
      if $enrolled; then
        echo 'Installation failed; previous executable and unit were restored. The newly enrolled configuration was retained because the old identity has been revoked. Retry installation with a new enrollment command or restore the executable using the retained configuration.' >&2
      else
        echo 'Installation failed; previous executable, unit and configuration were restored.' >&2
      fi
    else
      echo "Rollback could not restore every file. Protected backup files remain in $scratch" >&2
    fi
  fi
  rm -f -- "$binary.new"
  if $restored; then
    rm -f -- "$scratch/agent" "$scratch/checksum" "$scratch/unit" "$scratch/config.previous" "$scratch/binary.previous" "$scratch/unit.previous"
    rmdir -- "$scratch" 2>/dev/null || true
  fi
  exit "$status"
}
trap cleanup EXIT
base="$server_url/api/agent/download/linux/$arch"
curl --fail --silent --show-error --proto "$protocols" --connect-timeout 15 --max-time 180 "$base" -o "$scratch/agent"
curl --fail --silent --show-error --proto "$protocols" --connect-timeout 15 --max-time 30 "$base?checksum=1" -o "$scratch/checksum"
read -r expected _ < "$scratch/checksum"
[[ $expected =~ ^[a-fA-F0-9]{64}$ ]] || { echo 'Invalid checksum response' >&2; exit 1; }
actual=$(sha256sum "$scratch/agent"); actual=${actual%% *}
[[ ${actual,,} == ${expected,,} ]] || { echo 'Agent checksum mismatch' >&2; exit 1; }
chmod 700 "$scratch/agent"
"$scratch/agent" version

# Snapshot before changing config or the systemd unit, not just before the
# executable swap. cp -p also retains the configured runtime owner's access.
if [[ -f $binary ]]; then cp -p -- "$binary" "$scratch/binary.previous"; had_binary=true; fi
if [[ -f $unit ]]; then cp -p -- "$unit" "$scratch/unit.previous"; had_unit=true; fi
if [[ -f $config ]]; then cp -p -- "$config" "$scratch/config.previous"; had_config=true; fi
if systemctl is-enabled --quiet "$service.service" 2>/dev/null; then was_enabled=true; fi
if systemctl is-active --quiet "$service.service" 2>/dev/null; then was_active=true; fi

if [[ $action == install || $action == reenroll ]]; then
  [[ $action == reenroll || ! -e $config ]] || { echo 'Already enrolled; use upgrade to preserve identity or reenroll with a new ticket' >&2; exit 1; }
  enroll_flags=()
  if [[ $action == reenroll && -f $config ]]; then
    enroll_flags+=(--force)
  fi
  changing=true
  mkdir -p -- "$config_dir"
  chmod 700 "$config_dir"
  if $ticket_stdin; then
    IFS= read -r ticket || [[ -n $ticket ]]
  elif [[ -z $ticket ]]; then
    read -rsp 'Enrollment ticket: ' ticket </dev/tty
    echo
  fi
  [[ -n $ticket ]] || { echo 'Enrollment ticket is required' >&2; exit 2; }
  printf '%s\n' "$ticket" | "$scratch/agent" enroll --server-url "$server_url" --ticket-stdin --config "$config" "${enroll_flags[@]}"
  enrolled=true
  unset ticket
  chown "$run_user" "$config_dir" "$config"
  chmod 600 "$config"
  cat > "$scratch/unit" <<EOF
[Unit]
Description=Server Monitor outbound agent
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=$run_user
ExecStart=$binary run --config $config
Restart=always
RestartSec=5
KillMode=control-group
TimeoutStopSec=15
UMask=0077

[Install]
WantedBy=multi-user.target
EOF
  install -m 644 "$scratch/unit" "$unit"
fi
# Rename a complete new executable into place, retaining the old version until
# the replacement has passed its service health check.
"$scratch/agent" check --config "$config"
changing=true
install -m 755 "$scratch/agent" "$binary.new"
mv -f -- "$binary.new" "$binary"
systemctl daemon-reload
systemctl enable "$service.service"
systemctl restart "$service.service"
systemctl is-active --quiet "$service.service"
completed=true
echo 'Agent is running. Status: systemctl status server-monitor-agent; logs: journalctl -u server-monitor-agent -f'
