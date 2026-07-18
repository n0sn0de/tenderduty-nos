# Prometheus

Enable the exporter with existing compatibility keys:

```yaml
prometheus_enabled: true
prometheus_listen_port: 28686
```

The endpoint is `/metrics`. It has no built-in authentication; bind/publish it only on a trusted monitoring network.

## Compatibility contract

This foundation intentionally preserves the public `tenderduty_*` metric prefix and existing labels. Renaming metrics for brand purity would break dashboards and alert rules, so the migration table treats these names as a compatibility API. A future namespace migration must use parallel aliases and a deprecation window.

Current families cover:

- signed, proposed, and missed blocks;
- misses with prevote/precommit present;
- validator bonded/jailed/tombstoned state;
- consecutive misses and window totals;
- chain height and last block time;
- RPC node health/down duration.

Common labels include chain ID, chain display name, validator/operator identity, and node URL where applicable. Those labels can reveal operational topology; restrict scraper and dashboard access.

## Minimal scrape job

```yaml
scrape_configs:
  - job_name: nosnode-seer
    static_configs:
      - targets: ["127.0.0.1:28686"]
```

The target above is a public local default example, not a production topology recommendation.
