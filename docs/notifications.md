# Notifications

NosNode Seer preserves PagerDuty, Discord, Telegram, Slack, and healthcheck integrations. The alert body and routing/dedup semantics remain, while visible sender/summary identity is **NosNode🔮**.

## Two-level enablement

Each notification destination must be enabled globally and in a chain's `alerts` block. Chain-specific blank values inherit global credentials. This prevents a new chain from sending through every configured integration unintentionally.

## PagerDuty

Use an Events API v2 integration/routing key, not an account OAuth token. Put it in global `pagerduty.api_key` or a per-chain override. Preserve stable validator/dedup identity during migration so resolutions match triggers.

## Discord and Slack

Create an incoming webhook in a dedicated operator channel. Treat the full webhook URL as a secret. Mention lists are joined into alert content; use platform-supported identifiers and test in a non-production channel.

## Telegram

Create a bot, add it to the intended chat/channel, and configure the bot token plus channel identifier. Both are sensitive. Confirm bot permissions without granting unrelated administrative capabilities.

## Healthcheck

The optional dead-man's-switch sends periodic HTTP pings to `ping_url`. The URL commonly embeds a secret identifier. It confirms process activity, not validator health, and does not replace alert integrations.

## Delivery guarantees and retry policy

Delivery state is committed only after a destination accepts the request. A rejected or failed request releases its in-flight reservation, so a later emission of the same alert remains eligible. Resolutions are sent only to destinations that previously accepted the matching trigger, and a failed resolution leaves that destination open for a later retry. Alert message text and existing per-destination dedup keys are unchanged.

Every network attempt has a five-second deadline. Slack, Discord, and Telegram make one bounded send attempt per emitted event and do not retry in-process: after an ambiguous transport failure the remote service may have accepted the message, so an automatic retry could duplicate it. If the same event is emitted again, it is eligible for another attempt. This is **at-least-once**, not exactly-once, behavior; an ambiguous failure followed by a later retry can produce a duplicate.

PagerDuty makes at most two five-second attempts separated by a fixed 250 ms delay. Both attempts retain the existing Events API v2 dedup key, allowing PagerDuty to coalesce an ambiguous retry. This does not extend exactly-once guarantees beyond PagerDuty's API contract.

A single ordered worker drains a bounded 64-event in-memory channel. It fans each event out to at most four fixed destination workers, waits for them, then processes the next event so trigger/resolution order is preserved per destination. There is no unbounded goroutine or queue and no durable outbox: process termination can lose queued events, and a full queue applies backpressure to alert producers.

Delivery logs and metrics contain only the fixed destination and outcome values. They intentionally omit webhook URLs, bot tokens, routing keys, channels, chain/validator identity, and payload text.

## Safe rollout

1. Keep all integrations disabled while validating config and dashboard locally.
2. Enable one global destination and one non-production chain destination.
3. Trigger only a controlled local/fake integration test; do not deliberately impair a production validator.
4. Observe sender identity, dedup/resolution behavior, and secret redaction.
5. Enable remaining destinations deliberately.

No integration credentials belong in issues, pull requests, screenshots, logs, or example files.
