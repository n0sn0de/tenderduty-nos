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

## Safe rollout

1. Keep all integrations disabled while validating config and dashboard locally.
2. Enable one global destination and one non-production chain destination.
3. Trigger only a controlled local/fake integration test; do not deliberately impair a production validator.
4. Observe sender identity, dedup/resolution behavior, and secret redaction.
5. Enable remaining destinations deliberately.

No integration credentials belong in issues, pull requests, screenshots, logs, or example files.
