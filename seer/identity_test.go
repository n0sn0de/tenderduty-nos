package seer

import (
	"strings"
	"testing"
)

func TestRuntimeIdentity(t *testing.T) {
	if ProductName != "NosNode Seer" {
		t.Fatalf("ProductName = %q", ProductName)
	}
	if BrandName != "NosNode🔮" {
		t.Fatalf("BrandName = %q", BrandName)
	}
}

func TestAlertBuildersUseNosNodeBrand(t *testing.T) {
	msg := &alertMsg{chain: "test-chain", message: "missed block", slkMentions: "@operator"}

	slack := buildSlackMessage(msg)
	if len(slack.Attachments) != 1 || !strings.Contains(slack.Attachments[0].Title, BrandName) {
		t.Fatalf("Slack title is not branded: %+v", slack)
	}

	discord := buildDiscordMessage(msg)
	if discord.Username != BrandName {
		t.Fatalf("Discord username = %q, want %q", discord.Username, BrandName)
	}

	if got := buildTelegramText(msg); !strings.Contains(got, BrandName) {
		t.Fatalf("Telegram text is not branded: %q", got)
	}

	event := buildPagerDutyEvent(msg)
	if event.Payload == nil || !strings.Contains(event.Payload.Summary, BrandName) {
		t.Fatalf("PagerDuty summary is not branded: %+v", event)
	}
	if event.DedupKey != msg.uniqueId {
		t.Fatalf("PagerDuty dedup key changed: %q", event.DedupKey)
	}
}
