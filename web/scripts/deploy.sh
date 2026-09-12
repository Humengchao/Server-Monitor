#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
mode=${1:?mode required}
sha=${2:?commit required}
prefix=${3:-}
root=${DEPLOY_ROOT:-/opt/server-monitor}
[[ $sha =~ ^[a-f0-9]{40}$ ]] || { echo 'Invalid commit'; exit 2; }
[[ $root = /* ]] || exit 2
mkdir -p "$root/shared" "$root/releases"
exec 9>"$root/shared/deploy.lock"
flock -w 900 9
release="$root/releases/$sha"
config="$root/shared/.env"
project=server-monitor
unset BACKEND_IMAGE FRONTEND_IMAGE
if [[ ! -f $config ]]; then
  [[ -f $root/web/.env ]] || { echo 'Configure shared/.env before the first deployment'; exit 2; }
  cp "$root/web/.env" "$config"
fi
chmod 600 "$config"
restore() {
  if [[ -s $release/rollback.yml ]]; then
    docker compose --project-name "$project" -f "$release/rollback.yml" up -d --remove-orphans --pull never --wait --wait-timeout 180 || return 1
  else
    docker compose --project-name "$project" --env-file "$config" --env-file "$release/images.env" -f "$release/web/docker-compose.yml" stop || return 1
  fi
  if [[ -s $release/previous ]]; then
    ln -sfn "$(cat "$release/previous")" "$root/.current-$sha" || return 1
    mv -Tf "$root/.current-$sha" "$root/current" || return 1
  elif [[ -L $root/current && $(readlink "$root/current") = "$release" ]]; then
    unlink "$root/current" || return 1
  fi
}
if [[ $mode = rollback ]]; then
  [[ -L $root/current && $(readlink "$root/current") = "$release" ]] || { echo 'Release is no longer current; rollback skipped'; exit 0; }
  if ! restore; then echo 'ROLLBACK FAILED: operator intervention required'; exit 1; fi
  echo "Rolled back $sha"
  exit 0
fi
[[ $mode = deploy && $prefix =~ ^ghcr.io/[a-z0-9._/-]+$ ]] || exit 2
if [[ -L $root/current && $(readlink "$root/current") = "$release" ]]; then
  echo 'This release is already deployed'
  exit 0
fi
mkdir -p "$release"
if [[ -e $release/web ]]; then
  [[ -f $release/commit && $(cat "$release/commit") = "$sha" ]] || exit 2
else
  git -C "$root" archive "$sha" | tar -x -C "$release"
  printf '%s\n' "$sha" > "$release/commit"
fi
printf 'BACKEND_IMAGE=%s/backend:%s
FRONTEND_IMAGE=%s/frontend:%s
' "$prefix" "$sha" "$prefix" "$sha" > "$release/images.env"
: > "$release/previous"
: > "$release/rollback.yml"
old="$root/web/docker-compose.yml"
if [[ -L $root/current ]]; then
  readlink "$root/current" > "$release/previous"
  old="$root/current/web/docker-compose.yml"
fi
running=$(docker ps -q --filter label=com.docker.compose.project=$project --filter label=com.docker.compose.service=backend)
if [[ -n $running ]]; then
  if [[ -L $root/current ]]; then
    docker compose --project-name "$project" --env-file "$config" --env-file "$root/current/images.env" -f "$old" config > "$release/previous.yml"
  else
    docker compose --project-name "$project" --env-file "$config" -f "$old" config > "$release/previous.yml"
  fi
  printf 'services:
' > "$release/rollback-images.yml"
  for service in backend frontend; do
    container=$(docker ps -aq --filter label=com.docker.compose.project=$project --filter label=com.docker.compose.service=$service)
    [[ -n $container ]] || { echo "Missing previous $service container"; exit 2; }
    image=$(docker inspect --format '{{.Image}}' "$container")
    tag="server-monitor-rollback/$service:$sha"
    docker image tag "$image" "$tag"
    printf '  %s:
    image: %s
' "$service" "$tag" >> "$release/rollback-images.yml"
  done
  docker compose --project-name "$project" -f "$release/previous.yml" -f "$release/rollback-images.yml" config > "$release/rollback.yml"
fi
compose=(docker compose --project-name "$project" --env-file "$config" --env-file "$release/images.env" -f "$release/web/docker-compose.yml")
"${compose[@]}" config --quiet
"${compose[@]}" pull
failed() {
  trap - ERR
  echo 'Deployment verification failed; restoring previous containers'
  if ! restore; then echo 'ROLLBACK FAILED: operator intervention required'; fi
  exit 1
}
trap failed ERR
"${compose[@]}" up -d --remove-orphans --pull never --wait --wait-timeout 180
curl --fail --silent --show-error --max-time 20 http://127.0.0.1:19080/api/ready > /dev/null
for route in login dashboard docker files; do
  curl --fail --silent --show-error --max-time 20 "http://127.0.0.1:19080/$route" | grep -q 'id="root"'
done
ln -sfn "$release" "$root/.current-$sha"
mv -Tf "$root/.current-$sha" "$root/current"
trap - ERR
echo "Deployed $sha; persistent configuration left unchanged"
