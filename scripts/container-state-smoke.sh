#!/usr/bin/env bash
set -euo pipefail

runtime=${CONTAINER_RUNTIME:-docker}
image=${1:-nosnode-seer:ci}
workdir=$(mktemp -d)
prefix="seer-state-contracts-$$"
active=""
containers=()

runtime_extra=()
if [[ "$runtime" == "podman" ]]; then
  runtime_extra+=(--userns=keep-id:uid=65532,gid=65532)
fi

cleanup() {
  if [[ -n "$active" ]]; then
    "$runtime" kill "$active" >/dev/null 2>&1 || true
  fi
  for container in "${containers[@]}"; do
    "$runtime" rm -f "$container" >/dev/null 2>&1 || true
  done
  if ! rm -rf "$workdir" 2>/dev/null; then
    sudo rm -rf "$workdir"
  fi
}
trap cleanup EXIT INT TERM

mkdir -p "$workdir/canonical" "$workdir/legacy" "$workdir/explicit"
chmod 0777 "$workdir/canonical" "$workdir/legacy" "$workdir/explicit"

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
    nodes: []
YAML
  chmod 0644 "$directory/config.yml"
}
write_config "$workdir/canonical"
write_config "$workdir/legacy"
write_config "$workdir/explicit"

cat >"$workdir/legacy/.tenderduty-state.json" <<'JSON'
{
  "alarms": {
    "sent_pd_alarms": {},
    "sent_tg_alarms": {},
    "sent_di_alarms": {},
    "sent_slk_alarms": {},
    "sent_all_alarms": {}
  },
  "blocks": {"offline-state-fixture": [1, 0, -1]},
  "nodes_down": {}
}
JSON
chmod 0644 "$workdir/legacy/.tenderduty-state.json"

wait_for_log() {
  local container=$1
  local pattern=$2
  local attempt
  for attempt in $(seq 1 100); do
    local logs
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
    --user 65532:65532 \
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

run_python() {
  local file=$1
  shift
  if [[ -r "$file" ]]; then
    python3 "$@"
  else
    sudo python3 "$@"
  fi
}

assert_state() {
  local file=$1
  if [[ ! -s "$file" ]] && ! sudo test -s "$file" 2>/dev/null; then
    echo "missing state file: $file" >&2
    return 1
  fi
  run_python "$file" - "$file" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as handle:
    state = json.load(handle)
assert state["version"] == 1, state
assert set(("alarms", "blocks", "nodes_down")).issubset(state), state
PY
  local mode owner
  if [[ -r "$file" ]]; then
    mode=$(stat -c '%a' "$file")
    owner=$(stat -c '%u:%g' "$file")
  else
    mode=$(sudo stat -c '%a' "$file")
    owner=$(sudo stat -c '%u:%g' "$file")
  fi
  [[ "$mode" == "600" ]]
  if [[ "$runtime" == "docker" ]]; then
    [[ "$owner" == "65532:65532" ]]
  fi
  printf 'state-file: path=%s mode=%s owner=%s\n' "${file#$workdir/}" "$mode" "$owner"
}

corrupt_state() {
  local file=$1
  if [[ -w "$file" ]]; then
    printf 'interrupted\n' >"$file"
  else
    printf 'interrupted\n' | sudo tee "$file" >/dev/null
  fi
}

[[ $("$runtime" image inspect "$image" --format '{{.Config.User}}') == "65532:65532" ]]
[[ $("$runtime" image inspect "$image" --format '{{json .Config.Entrypoint}}') == '["/usr/local/bin/nosnode-seer"]' ]]
printf 'runtime-contract: user=65532:65532 entrypoint=/usr/local/bin/nosnode-seer\n'

start_container canonical-first "$workdir/canonical" /var/lib/nosnode-seer ""
wait_for_log "$active" 'config is valid'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_state "$workdir/canonical/.nosnode-seer-state.json"
printf 'state-write: canonical default persisted after SIGTERM with network disabled\n'

start_container canonical-reload "$workdir/canonical" /var/lib/nosnode-seer ""
wait_for_log "$active" 'loaded durable state version 1'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_state "$workdir/canonical/.nosnode-seer-state.json"
assert_state "$workdir/canonical/.nosnode-seer-state.json.bak"
printf 'state-reload: restart loaded version 1 and retained rollback backup\n'

corrupt_state "$workdir/canonical/.nosnode-seer-state.json"
start_container canonical-rollback "$workdir/canonical" /var/lib/nosnode-seer ""
wait_for_log "$active" 'recovered durable state from rollback backup'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_state "$workdir/canonical/.nosnode-seer-state.json"
assert_state "$workdir/canonical/.nosnode-seer-state.json.bak"
printf 'rollback-recovery: malformed primary recovered without replacing known-good backup\n'

start_container explicit-state "$workdir/explicit" /var/lib/nosnode-seer "" -state explicit-state.json
wait_for_log "$active" 'config is valid'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_state "$workdir/explicit/explicit-state.json"
[[ ! -e "$workdir/explicit/.nosnode-seer-state.json" ]]
printf 'explicit-state: -state path won over both default filenames\n'

start_container legacy-alias "$workdir/legacy" /var/lib/tenderduty /bin/tenderduty
wait_for_log "$active" 'using legacy state file .tenderduty-state.json'
wait_for_log "$active" 'loaded legacy durable state'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_state "$workdir/legacy/.tenderduty-state.json"
if [[ -r "$workdir/legacy/.tenderduty-state.json.bak" ]]; then
  grep -Fq '"blocks"' "$workdir/legacy/.tenderduty-state.json.bak"
else
  sudo grep -Fq '"blocks"' "$workdir/legacy/.tenderduty-state.json.bak"
fi
[[ ! -e "$workdir/legacy/.nosnode-seer-state.json" ]]
printf 'legacy-shim: /bin/tenderduty loaded config and legacy state from /var/lib/tenderduty\n'

start_container legacy-volume "$workdir/legacy" /var/lib/tenderduty ""
wait_for_log "$active" 'loaded durable state version 1'
wait_for_log "$active" 'durable state checkpoint handler ready'
stop_cleanly
assert_state "$workdir/legacy/.tenderduty-state.json"
printf 'legacy-volume: canonical entrypoint discovered the existing /var/lib/tenderduty mount\n'

printf 'network-contract: every runtime probe used --network none; fixture declares no RPC nodes\n'
