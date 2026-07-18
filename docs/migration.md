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
| State schema | Tenderduty JSON | Preserved | No conversion. |
| State default | `.tenderduty-state.json` | `.nosnode-seer-state.json` | Automatic legacy fallback when only the old default exists; explicit `-state` always wins. |
| Metrics | `tenderduty_*` names and labels | Preserved exactly | Do not rewrite dashboards/alerts. |
| Dashboard HTTP | `/`, `/state`, `/logs`, `/logsenabled`, `/ws` | Preserved | Theme/copy changed; consumers keep endpoints. |
| Dashboard assets | UIkit/Lodash and upstream visuals | First-party CSS/vanilla JS, no visual bundle | Clear browser cache after cutover. |
| Alert identity | Tenderduty wording | `NosNode🔮` in Slack, Discord, Telegram, and PagerDuty summaries | Routing/dedup keys remain unchanged. |
| Go module | `github.com/blockpane/tenderduty/v2` | `github.com/n0sn0de/tenderduty-nos` | Downstream Go imports must update; this executable did not promise a stable library API. |
| Go package path | `/td2` | `/seer` | Update downstream imports if any. |
| Container user | root-capable legacy image | numeric non-root `65532:65532`, scratch runtime | Ensure mounted state storage is writable by that UID/GID. |

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

Keep the original binary/image, config backup, and state backup until alerting and metrics are observed in a non-production test. Rollback is executable replacement plus the original explicit state path; the schema was not rewritten.
