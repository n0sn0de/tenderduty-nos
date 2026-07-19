#!/usr/bin/env bash
set -euo pipefail

runtime=${CONTAINER_RUNTIME:-docker}
image=${1:-nosnode-seer:ci}
workdir=$(mktemp -d)
prefix="seer-state-contracts-$$"
active=""
containers=()
legacy_uid=26657
legacy_gid=26657
runtime_extra=()
needs_privileged_owner=true

if [[ "$runtime" == "podman" ]] && [[ $("$runtime" info --format '{{.Host.Security.Rootless}}') == "true" ]]; then
  runtime_extra+=(--userns="keep-id:uid=${legacy_uid},gid=${legacy_gid}")
  needs_privileged_owner=false
fi

as_privileged() {
  if [[ $(id -u) == "0" ]] || [[ "$needs_privileged_owner" == "false" ]]; then
    "$@"
  else
    sudo -n "$@"
  fi
}

cleanup() {
  if [[ -n "$active" ]]; then
    "$runtime" kill "$active" >/dev/null 2>&1 || true
  fi
  for container in "${containers[@]}"; do
    "$runtime" rm -f "$container" >/dev/null 2>&1 || true
  done
  as_privileged rm -rf "$workdir" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

mkdir -p "$workdir/canonical" "$workdir/legacy" "$workdir/explicit"

write_config() {
  local directory=$1
  cat >"$directory/config.yml" <<'YAML'
enable_dashboard: false
prometheus_enabled: false
chains:
  offline-state-fixture:
    chain_id: offline-state-1
    valoper_address: offlinevaloper1statefixture
    public_fallback: false
    alerts:
      stalled_enabled: false
      consecutive_enabled: false
      percentage_enabled: false
      alert_if_inactive: false
      alert_if_no_servers: false
    nodes:
      - url: http://127.0.0.1:26657
        alert_if_down: false
YAML
  chmod 0644 "$directory/config.yml"
}

write_semantic_fixture() {
  local path=$1
  local mode=$2
  cat >"$path" <<'JSON'
{
  "alarms": {
    "sent_pd_alarms": {"sentinel-delivery-id": "2099-01-02T03:04:05Z"},
    "sent_tg_alarms": {"sentinel-delivery-id": "2099-01-02T03:04:05Z"},
    "sent_di_alarms": {"sentinel-delivery-id": "2099-01-02T03:04:05Z"},
    "sent_slk_alarms": {"sentinel-delivery-id": "2099-01-02T03:04:05Z"},
    "sent_all_alarms": {
      "offline-state-fixture": {
        "sentinel-dashboard-alarm": "2099-02-03T04:05:06Z"
      }
    }
  },
  "blocks": {"offline-state-fixture": [4, 3, 2, 1, -1]},
  "nodes_down": {
    "offline-state-fixture": {
      "http://127.0.0.1:26657": "2026-01-02T03:04:05Z"
    }
  }
}
JSON
  chmod "$mode" "$path"
}

for directory in canonical legacy explicit; do
  write_config "$workdir/$directory"
done
write_semantic_fixture "$workdir/canonical/.nosnode-seer-state.json" 0600
write_semantic_fixture "$workdir/legacy/.tenderduty-state.json" 0644
write_semantic_fixture "$workdir/explicit/explicit-state.json" 0600
chmod 0755 "$workdir/canonical" "$workdir/legacy" "$workdir/explicit"

if [[ "$needs_privileged_owner" == "true" ]]; then
  as_privileged chown -R "${legacy_uid}:${legacy_gid}" "$workdir/canonical" "$workdir/legacy" "$workdir/explicit"
fi

host_stat() {
  as_privileged stat -c "$1" "$2"
}

assert_legacy_fixture_permissions() {
  local directory=$1
  local state=$2
  local expected_state_mode=$3
  [[ $(host_stat '%a' "$directory") == "755" ]]
  [[ $(host_stat '%a' "$directory/config.yml") == "644" ]]
  [[ $(host_stat '%a' "$state") == "$expected_state_mode" ]]
  if [[ "$needs_privileged_owner" == "true" ]]; then
    [[ $(host_stat '%u:%g' "$directory") == "${legacy_uid}:${legacy_gid}" ]]
    [[ $(host_stat '%u:%g' "$directory/config.yml") == "${legacy_uid}:${legacy_gid}" ]]
    [[ $(host_stat '%u:%g' "$state") == "${legacy_uid}:${legacy_gid}" ]]
  fi
  printf 'legacy-permissions: dir=%s config=644 state=%s runtime-owner=%s:%s\n' \
    "${directory#$workdir/}" "$expected_state_mode" "$legacy_uid" "$legacy_gid"
}

assert_legacy_fixture_permissions "$workdir/canonical" "$workdir/canonical/.nosnode-seer-state.json" 600
assert_legacy_fixture_permissions "$workdir/legacy" "$workdir/legacy/.tenderduty-state.json" 644
assert_legacy_fixture_permissions "$workdir/explicit" "$workdir/explicit/explicit-state.json" 600

wait_for_log() {
  local container=$1
  local pattern=$2
  local attempt logs
  for attempt in $(seq 1 150); do
    logs=$("$runtime" logs "$container" 2>&1 || true)
    if grep -Fq "$pattern" <<<"$logs"; then
      return 0
    fi
    if [[ $("$runtime" inspect "$container" --format '{{.State.Running}}' 2>/dev/null || true) != "true" ]]; then
      "$runtime" logs "$container" >&2 || true
      return 1
    fi
    sleep 0.1
  done
  "$runtime" logs "$container" >&2 || true
  return 1
}

start_container() {
  local suffix=$1
  local mount_source=$2
  local mount_target=$3
  local entrypoint=$4
  shift 4
  active="${prefix}-${suffix}"
  containers+=("$active")
  local entrypoint_args=()
  if [[ -n "$entrypoint" ]]; then
    entrypoint_args+=(--entrypoint "$entrypoint")
  fi
  "$runtime" run --detach --name "$active" \
    "${runtime_extra[@]}" \
    --network none \
    --read-only \
    --cap-drop all \
    --security-opt no-new-privileges \
    --pids-limit 128 \
    --user "${legacy_uid}:${legacy_gid}" \
    --volume "$mount_source:$mount_target:Z" \
    "${entrypoint_args[@]}" \
    "$image" "$@" >/dev/null
}

stop_cleanly() {
  local container=$active
  "$runtime" kill --signal TERM "$container" >/dev/null
  local status
  status=$("$runtime" wait "$container")
  if [[ "$status" != "0" ]]; then
    "$runtime" logs "$container" >&2 || true
    echo "container $container exited $status" >&2
    return 1
  fi
  active=""
}

assert_semantic_state() {
  local file=$1
  if ! as_privileged test -s "$file"; then
    echo "missing state file: $file" >&2
    return 1
  fi
  as_privileged python3 - "$file" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    state = json.load(handle)
assert state.get("version", 0) in (0, 1), state
alarms = state["alarms"]
for destination in ("sent_pd_alarms", "sent_tg_alarms", "sent_di_alarms", "sent_slk_alarms"):
    assert alarms[destination] == {"sentinel-delivery-id": "2099-01-02T03:04:05Z"}, alarms
assert alarms["sent_all_alarms"] == {
    "offline-state-fixture": {"sentinel-dashboard-alarm": "2099-02-03T04:05:06Z"}
}, alarms
assert state["blocks"] == {"offline-state-fixture": [4, 3, 2, 1, -1]}, state
assert state["nodes_down"] == {
    "offline-state-fixture": {"http://127.0.0.1:26657": "2026-01-02T03:04:05Z"}
}, state
PY
  local mode owner
  mode=$(host_stat '%a' "$file")
  owner=$(host_stat '%u:%g' "$file")
  [[ "$mode" == "600" ]]
  if [[ "$needs_privileged_owner" == "true" ]]; then
    [[ "$owner" == "${legacy_uid}:${legacy_gid}" ]]
  fi
  printf 'semantic-state: path=%s mode=%s owner=%s\n' "${file#$workdir/}" "$mode" "$owner"
}

assert_primary_and_backup() {
  assert_semantic_state "$1"
  assert_semantic_state "$1.bak"
}

corrupt_state() {
  local file=$1
  as_privileged sh -c 'printf "interrupted\n" >"$1"' sh "$file"
}

[[ $("$runtime" image inspect "$image" --format '{{.Config.User}}') == "${legacy_uid}:${legacy_gid}" ]]
[[ $("$runtime" image inspect "$image" --format '{{json .Config.Entrypoint}}') == '["/usr/local/bin/nosnode-seer"]' ]]
printf 'runtime-contract: user=%s:%s entrypoint=/usr/local/bin/nosnode-seer\n' "$legacy_uid" "$legacy_gid"

start_container canonical-first "$workdir/canonical" /var/lib/nosnode-seer ""
wait_for_log "$active" 'loaded legacy durable state'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_primary_and_backup "$workdir/canonical/.nosnode-seer-state.json"
printf 'canonical-checkpoint: semantic fixture and rollback backup survived SIGTERM\n'

start_container canonical-reload "$workdir/canonical" /var/lib/nosnode-seer ""
wait_for_log "$active" 'loaded durable state version 1'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_primary_and_backup "$workdir/canonical/.nosnode-seer-state.json"
printf 'canonical-restart: versioned state and semantic backup survived restart\n'

corrupt_state "$workdir/canonical/.nosnode-seer-state.json"
start_container canonical-rollback "$workdir/canonical" /var/lib/nosnode-seer ""
wait_for_log "$active" 'recovered durable state from rollback backup'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_primary_and_backup "$workdir/canonical/.nosnode-seer-state.json"
printf 'rollback-recovery: malformed primary recovered without poisoning known-good backup\n'

start_container explicit-first "$workdir/explicit" /var/lib/nosnode-seer "" -state explicit-state.json
wait_for_log "$active" 'loaded legacy durable state'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_primary_and_backup "$workdir/explicit/explicit-state.json"
[[ ! -e "$workdir/explicit/.nosnode-seer-state.json" ]]

start_container explicit-reload "$workdir/explicit" /var/lib/nosnode-seer "" -state explicit-state.json
wait_for_log "$active" 'loaded durable state version 1'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_primary_and_backup "$workdir/explicit/explicit-state.json"
printf 'explicit-state: semantic -state path survived checkpoint and restart\n'

start_container legacy-alias "$workdir/legacy" /var/lib/tenderduty /bin/tenderduty
wait_for_log "$active" 'using legacy state file .tenderduty-state.json'
wait_for_log "$active" 'loaded legacy durable state'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_primary_and_backup "$workdir/legacy/.tenderduty-state.json"
[[ ! -e "$workdir/legacy/.nosnode-seer-state.json" ]]
printf 'legacy-alias: /bin/tenderduty preserved semantic state on old 0644 volume\n'

start_container legacy-alias-reload "$workdir/legacy" /var/lib/tenderduty /bin/tenderduty
wait_for_log "$active" 'loaded durable state version 1'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_primary_and_backup "$workdir/legacy/.tenderduty-state.json"
printf 'legacy-alias-restart: /bin/tenderduty restarted and retained semantic backup\n'

start_container legacy-volume "$workdir/legacy" /var/lib/tenderduty ""
wait_for_log "$active" 'loaded durable state version 1'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_primary_and_backup "$workdir/legacy/.tenderduty-state.json"
printf 'legacy-volume: canonical entrypoint discovered and restarted the historical mount\n'

printf 'hardening-contract: all probes used read-only rootfs, network none, no capabilities, no-new-privileges\n'
