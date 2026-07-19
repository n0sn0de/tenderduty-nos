# Migration from Tenderduty

Stop the old process, back up config and state, then test Seer with an explicit copied state file before replacing the service. Do not run both processes with the same notification credentials unless duplicate alerts are acceptable.

## Compatibility table

| Surface | Tenderduty behavior | NosNode Seer foundation | Action |
|---|---|---|---|
| Binary/container command | `tenderduty` | `nosnode-seer` | Update service/compose command. |
| YAML keys and nesting | Existing v2 names | Preserved and covered by compatibility tests | No key rename. |
| Main config default | `config.yml` | Preserved | None. |
| Chain directory default | `chains.d` | Preserved | None. |
| CLI flags | `-f`, `-cc`, `-state`, encryption flags | Preserved; `-version` added | Update executable only. |
| Environment | `CONFIG`, `PASSWORD` | Preserved | Prefer runtime secret files when possible. |
| State schema | Tenderduty JSON | Legacy fields preserved; new checkpoints add only top-level `"version": 1` | No pre-start conversion. An old writer can read versioned state but may remove the additive field when it rewrites the file. |
| State default | `.tenderduty-state.json` | `.nosnode-seer-state.json` | Automatic legacy fallback when only the old default exists; explicit `-state` always wins. |
| Metrics | `tenderduty_*` names and labels | Preserved exactly | Do not rewrite dashboards/alerts. |
| Dashboard HTTP | `/`, `/state`, `/logs`, `/logsenabled`, `/ws` | Preserved | Theme/copy changed; consumers keep endpoints. |
| Dashboard assets | UIkit/Lodash and upstream visuals | First-party CSS/vanilla JS, no visual bundle | Clear browser cache after cutover. |
| Alert identity | Tenderduty wording | `NosNode🔮` in Slack, Discord, Telegram, and PagerDuty summaries | Routing/dedup keys remain unchanged. |
| Alert delivery state | Destination marked sent before network acceptance; unbounded per-event goroutines | Commit after acceptance, bounded deadlines/workers, destination-safe retries | No YAML change. Review [delivery guarantees](notifications.md#delivery-guarantees-and-retry-policy); queued events remain in-memory only. |
| Go module | `github.com/blockpane/tenderduty/v2` | `github.com/n0sn0de/tenderduty-nos` | Downstream Go imports must update; this executable did not promise a stable library API. |
| Go package path | `/td2` | `/seer` | Update downstream imports if any. |
| Container user | historical numeric non-root `26657:26657` | retained as `26657:26657` in the scratch runtime for this migration cycle | Existing directories owned by `26657:26657` at mode `0755`, configs/state at `0644`, and private state at `0600` remain usable. |

## Deterministic state fallback

When `-state` is omitted:

1. use `.nosnode-seer-state.json` if it exists;
2. otherwise use `.tenderduty-state.json` if it exists and print a notice;
3. otherwise create/use `.nosnode-seer-state.json` on shutdown.

Passing `-state PATH` bypasses fallback. To migrate deliberately:

```sh
cp .tenderduty-state.json .nosnode-seer-state.json
nosnode-seer -f config.yml -cc chains.d -state .nosnode-seer-state.json
```

## Rollback

Keep the original binary/image, config backup, and state backup until alerting and metrics are observed in a non-production test. New checkpoints preserve the legacy fields and add `"version": 1`; they do not wrap the document. A rolled-back Tenderduty writer is expected to ignore that unknown field and may remove it on its next write. Seer subsequently treats that unversioned document as version 0. Keep an operator-owned copy because the old writer does not provide Seer's atomic `.bak` checkpoint contract.

## Durable state and rollback contract

NosNode Seer keeps the legacy top-level state shape and adds only a top-level
`"version": 1` field on new writes:

```json
{
  "version": 1,
  "alarms": {},
  "blocks": {},
  "nodes_down": {}
}
```

This is deliberately **not** a wrapping envelope. Existing Tenderduty binaries
ignore the unknown `version` field and continue to read `alarms`, `blocks`, and
`nodes_down` in place. NosNode Seer accepts unversioned legacy documents as
version 0, preserving block history, node-down history, accepted-delivery alarm
history, and dashboard alarm history. Unsupported future versions are rejected
instead of silently misread.

State selection remains compatible: explicit `-state PATH` wins; otherwise the
new default is preferred; otherwise the legacy default is used in place. With
neither default present, startup uses empty state and the first clean shutdown
creates `.nosnode-seer-state.json`.

Each checkpoint is written to a mode `0600` temporary file in the destination
directory. Seer encodes and flushes the document, calls file `fsync`, closes the
temporary file, renames it over the destination, then calls directory `fsync`.
The destination is never truncated in place and any failure is returned to the
caller, causing a non-zero process exit.

For an existing valid primary, the exact previous primary is first installed
atomically as `PATH.bak`; only then is the new primary installed:

| Primary before startup/checkpoint | Backup | Behavior |
| --- | --- | --- |
| Missing | Missing | Start empty; first checkpoint creates only the primary. |
| Valid | Any | Load primary; checkpoint installs that primary as `.bak` before replacing it. |
| Missing, empty, or malformed | Valid | Load `.bak` and report recovery; checkpoint repairs primary without overwriting the known-good backup. |
| Empty or malformed | Missing or invalid | Stop with an error; do not silently discard durable state. |
| Unsupported future version | Missing or invalid | Stop with an error. |
| Backup replacement fails | Existing valid primary | Return an error and leave primary untouched. |
| Primary replacement fails after backup succeeds | Valid primary and backup | Return an error; both still contain the last known-good primary. |

A directory-`fsync` failure is also returned. As with every POSIX atomic rename,
the rename may already be visible when a post-rename directory sync reports an
error; the rollback copy remains available.

## Container migration bridge

The canonical executable and writable state location are
`/usr/local/bin/nosnode-seer` and `/var/lib/nosnode-seer`.

For one migration cycle, the image also includes an executable deprecated alias
at `/bin/tenderduty` and a deprecated `/var/lib/tenderduty` directory. Invoking
the deprecated alias selects the deprecated directory as its working directory.
The canonical entrypoint also selects it when running from the canonical image
workdir, no explicit `-f` or `CONFIG` is set, canonical `config.yml` is absent,
and the deprecated mount contains `config.yml`. Therefore an existing volume
containing `config.yml` and `.tenderduty-state.json` continues to work without
data conversion:

```sh
docker run --rm \
  --volume tenderduty-data:/var/lib/tenderduty \
  --entrypoint /bin/tenderduty \
  ghcr.io/n0sn0de/nosnode-seer:TAG
```

The process identity, logs, and version output remain NosNode Seer. This bridge
is deprecated: update the entrypoint and volume destination to the canonical
paths before the next breaking container migration. Container CI exercises
clean SIGTERM, restart, backup recovery, explicit state, and the deprecated
bridge with `--network none`, a read-only root filesystem, no capabilities,
`no-new-privileges`, and the retained UID/GID `26657:26657`. The smoke uses
actual legacy directory/file modes (`0755`, `0644`, and `0600`) and verifies the
sentinel alarm IDs/timestamps, block history, and node-down timestamp in both
primary and backup files across checkpoint, restart, and recovery.

A future UID/GID transition is a separate breaking migration. It must ship with
an explicit ownership-conversion procedure and equivalent rootful and rootless
volume tests; this compatibility cycle deliberately does not perform that
transition.
