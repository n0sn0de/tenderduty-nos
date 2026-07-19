package seer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/PagerDuty/go-pagerduty"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type alertMsg struct {
	pd   bool
	disc bool
	tg   bool
	slk  bool

	severity string
	resolved bool
	chain    string
	message  string
	uniqueId string
	key      string

	tgChannel  string
	tgKey      string
	tgMentions string

	discHook     string
	discMentions string

	slkHook     string
	slkMentions string
}

type notifyDest uint8

const (
	pd notifyDest = iota
	tg
	di
	slk
)

type alarmCache struct {
	SentPdAlarms   map[string]time.Time            `json:"sent_pd_alarms"`
	SentTgAlarms   map[string]time.Time            `json:"sent_tg_alarms"`
	SentDiAlarms   map[string]time.Time            `json:"sent_di_alarms"`
	SentSlkAlarms  map[string]time.Time            `json:"sent_slk_alarms"`
	AllAlarms      map[string]map[string]time.Time `json:"sent_all_alarms"`
	flappingAlarms map[string]map[string]time.Time
	inFlight       map[notifyDest]map[string]struct{}
	notifyMux      sync.RWMutex
}

func newAlarmCache() *alarmCache {
	return &alarmCache{
		SentPdAlarms:   make(map[string]time.Time),
		SentTgAlarms:   make(map[string]time.Time),
		SentDiAlarms:   make(map[string]time.Time),
		SentSlkAlarms:  make(map[string]time.Time),
		AllAlarms:      make(map[string]map[string]time.Time),
		flappingAlarms: make(map[string]map[string]time.Time),
		inFlight:       make(map[notifyDest]map[string]struct{}),
	}
}

func (a *alarmCache) MarshalJSON() ([]byte, error) {
	a.notifyMux.RLock()
	defer a.notifyMux.RUnlock()
	return json.Marshal(struct {
		SentPdAlarms  map[string]time.Time            `json:"sent_pd_alarms"`
		SentTgAlarms  map[string]time.Time            `json:"sent_tg_alarms"`
		SentDiAlarms  map[string]time.Time            `json:"sent_di_alarms"`
		SentSlkAlarms map[string]time.Time            `json:"sent_slk_alarms"`
		AllAlarms     map[string]map[string]time.Time `json:"sent_all_alarms"`
	}{
		SentPdAlarms:  a.SentPdAlarms,
		SentTgAlarms:  a.SentTgAlarms,
		SentDiAlarms:  a.SentDiAlarms,
		SentSlkAlarms: a.SentSlkAlarms,
		AllAlarms:     a.AllAlarms,
	})
}

func (a *alarmCache) clearNoBlocks(chain string) {
	a.notifyMux.Lock()
	defer a.notifyMux.Unlock()
	if a.AllAlarms == nil || a.AllAlarms[chain] == nil {
		return
	}
	for clearAlarm := range a.AllAlarms[chain] {
		if strings.HasPrefix(clearAlarm, "stalled: have not seen a new block on") {
			delete(a.AllAlarms[chain], clearAlarm)
		}
	}
}

func (a *alarmCache) getCount(chain string) int {
	a.notifyMux.RLock()
	defer a.notifyMux.RUnlock()
	if a.AllAlarms == nil || a.AllAlarms[chain] == nil {
		return 0
	}
	return len(a.AllAlarms[chain])
}

func (a *alarmCache) clearAll(chain string) {
	a.notifyMux.Lock()
	defer a.notifyMux.Unlock()
	if a.AllAlarms == nil || a.AllAlarms[chain] == nil {
		return
	}
	a.AllAlarms[chain] = make(map[string]time.Time)
}

// alarms is used to prevent double notifications. TODO: save on exit / load on start
var alarms = newAlarmCache()

func (a *alarmCache) sentMapLocked(dest notifyDest) map[string]time.Time {
	switch dest {
	case pd:
		if a.SentPdAlarms == nil {
			a.SentPdAlarms = make(map[string]time.Time)
		}
		return a.SentPdAlarms
	case tg:
		if a.SentTgAlarms == nil {
			a.SentTgAlarms = make(map[string]time.Time)
		}
		return a.SentTgAlarms
	case di:
		if a.SentDiAlarms == nil {
			a.SentDiAlarms = make(map[string]time.Time)
		}
		return a.SentDiAlarms
	case slk:
		if a.SentSlkAlarms == nil {
			a.SentSlkAlarms = make(map[string]time.Time)
		}
		return a.SentSlkAlarms
	default:
		panic("unknown notification destination")
	}
}

func (a *alarmCache) reserveDelivery(msg *alertMsg, dest notifyDest) bool {
	a.notifyMux.Lock()
	defer a.notifyMux.Unlock()
	sent := a.sentMapLocked(dest)
	if a.inFlight == nil {
		a.inFlight = make(map[notifyDest]map[string]struct{})
	}
	if a.inFlight[dest] == nil {
		a.inFlight[dest] = make(map[string]struct{})
	}
	if _, reserved := a.inFlight[dest][msg.message]; reserved {
		return false
	}
	if msg.resolved {
		if sent[msg.message].IsZero() {
			return false
		}
	} else if !sent[msg.message].IsZero() {
		return false
	}
	if dest == pd && !msg.resolved {
		if a.flappingAlarms == nil {
			a.flappingAlarms = make(map[string]map[string]time.Time)
		}
		if a.flappingAlarms[msg.chain] != nil && a.flappingAlarms[msg.chain][msg.message].After(time.Now().Add(-5*time.Minute)) {
			return false
		}
	}
	a.inFlight[dest][msg.message] = struct{}{}
	return true
}

func (a *alarmCache) completeDelivery(msg *alertMsg, dest notifyDest, accepted bool) {
	a.notifyMux.Lock()
	defer a.notifyMux.Unlock()
	if a.inFlight != nil && a.inFlight[dest] != nil {
		delete(a.inFlight[dest], msg.message)
	}
	if !accepted {
		return
	}
	sent := a.sentMapLocked(dest)
	if msg.resolved {
		delete(sent, msg.message)
		return
	}
	now := time.Now()
	sent[msg.message] = now
	if dest == pd {
		if a.flappingAlarms == nil {
			a.flappingAlarms = make(map[string]map[string]time.Time)
		}
		if a.flappingAlarms[msg.chain] == nil {
			a.flappingAlarms[msg.chain] = make(map[string]time.Time)
		}
		a.flappingAlarms[msg.chain][msg.message] = now
	}
}

type NotificationTransportError struct {
	Destination string
	cause       error
}

func (e *NotificationTransportError) Error() string {
	return fmt.Sprintf("%s notification transport failed", e.Destination)
}

func (e *NotificationTransportError) Unwrap() error {
	return e.cause
}

type NotificationAPIError struct {
	Destination string
	cause       error
}

func (e *NotificationAPIError) Error() string {
	return fmt.Sprintf("%s notification rejected by remote API", e.Destination)
}

func (e *NotificationAPIError) Unwrap() error {
	return e.cause
}

type NotificationHTTPError struct {
	Destination string
	StatusCode  int
}

func (e *NotificationHTTPError) Error() string {
	return fmt.Sprintf("%s notification rejected with HTTP status %d", e.Destination, e.StatusCode)
}

type notificationSender struct {
	cache             *alarmCache
	httpClient        *http.Client
	telegramEndpoint  string
	pagerDutyEndpoint string
	sleep             func(time.Duration)
	observe           func(string, string)
}

const (
	notificationAttemptTimeout = 5 * time.Second
	notificationQueueCapacity  = 64
	pagerDutyMaxAttempts       = 2
	pagerDutyRetryDelay        = 250 * time.Millisecond
	deliveryAccepted           = "accepted"
	deliveryRejected           = "rejected"
	deliveryTransportError     = "transport_error"
)

func newNotificationSender(cache *alarmCache, client *http.Client) *notificationSender {
	if cache == nil {
		cache = newAlarmCache()
	}
	if client == nil {
		client = &http.Client{}
	}
	boundedClient := *client
	boundedClient.Timeout = notificationAttemptTimeout
	return &notificationSender{
		cache:            cache,
		httpClient:       &boundedClient,
		telegramEndpoint: tgbotapi.APIEndpoint,
		sleep:            time.Sleep,
		observe:          observeNotificationDelivery,
	}
}

var notifications = newNotificationSender(alarms, nil)

func (s *notificationSender) observeError(destination string, err error) error {
	outcome := deliveryTransportError
	var httpErr *NotificationHTTPError
	var apiErr *NotificationAPIError
	if errors.As(err, &httpErr) || errors.As(err, &apiErr) {
		outcome = deliveryRejected
	}
	if s.observe != nil {
		s.observe(destination, outcome)
	}
	return err
}

func (s *notificationSender) observeAccepted(destination string) {
	if s.observe != nil {
		s.observe(destination, deliveryAccepted)
	}
}

func notifySlack(msg *alertMsg) error {
	return notifications.notifySlack(msg)
}

func (s *notificationSender) notifySlack(msg *alertMsg) (err error) {
	if !msg.slk {
		return nil
	}
	if !s.cache.reserveDelivery(msg, slk) {
		return nil
	}
	accepted := false
	defer func() { s.cache.completeDelivery(msg, slk, accepted) }()

	data, err := json.Marshal(buildSlackMessage(msg))
	if err != nil {
		return s.observeError("slack", &NotificationTransportError{Destination: "slack", cause: err})
	}
	ctx, cancel := context.WithTimeout(context.Background(), notificationAttemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, msg.slkHook, bytes.NewBuffer(data))
	if err != nil {
		return s.observeError("slack", &NotificationTransportError{Destination: "slack", cause: err})
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return s.observeError("slack", &NotificationTransportError{Destination: "slack", cause: err})
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s.observeError("slack", &NotificationHTTPError{Destination: "slack", StatusCode: resp.StatusCode})
	}
	accepted = true
	s.observeAccepted("slack")
	return nil
}

type SlackMessage struct {
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments"`
}

type Attachment struct {
	Text      string `json:"text"`
	Color     string `json:"color"`
	Title     string `json:"title"`
	TitleLink string `json:"title_link"`
}

func buildSlackMessage(msg *alertMsg) *SlackMessage {
	prefix := "🚨 ALERT: "
	color := "danger"
	text := msg.message
	if msg.resolved {
		text = "OK: " + text
		prefix = "💜 Resolved: "
		color = "good"
	}
	return &SlackMessage{
		Text: text,
		Attachments: []Attachment{
			{
				Title: fmt.Sprintf("%s %s %s %s", BrandName, prefix, msg.chain, msg.slkMentions),
				Color: color,
			},
		},
	}
}

func notifyDiscord(msg *alertMsg) error {
	return notifications.notifyDiscord(msg)
}

func (s *notificationSender) notifyDiscord(msg *alertMsg) (err error) {
	if !msg.disc {
		return nil
	}
	if !s.cache.reserveDelivery(msg, di) {
		return nil
	}
	accepted := false
	defer func() { s.cache.completeDelivery(msg, di, accepted) }()

	data, err := json.MarshalIndent(buildDiscordMessage(msg), "", "  ")
	if err != nil {
		return s.observeError("discord", &NotificationTransportError{Destination: "discord", cause: err})
	}
	ctx, cancel := context.WithTimeout(context.Background(), notificationAttemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, msg.discHook, bytes.NewBuffer(data))
	if err != nil {
		return s.observeError("discord", &NotificationTransportError{Destination: "discord", cause: err})
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return s.observeError("discord", &NotificationTransportError{Destination: "discord", cause: err})
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return s.observeError("discord", &NotificationHTTPError{Destination: "discord", StatusCode: resp.StatusCode})
	}
	accepted = true
	s.observeAccepted("discord")
	return nil
}

type DiscordMessage struct {
	Username  string         `json:"username,omitempty"`
	AvatarUrl string         `json:"avatar_url,omitempty"`
	Content   string         `json:"content"`
	Embeds    []DiscordEmbed `json:"embeds,omitempty"`
}

type DiscordEmbed struct {
	Title       string `json:"title,omitempty"`
	Url         string `json:"url,omitempty"`
	Description string `json:"description"`
	Color       uint   `json:"color"`
}

func buildDiscordMessage(msg *alertMsg) *DiscordMessage {
	prefix := "🚨 ALERT: "
	if msg.resolved {
		prefix = "💜 Resolved: "
	}
	return &DiscordMessage{
		Username: BrandName,
		Content:  prefix + msg.chain,
		Embeds: []DiscordEmbed{{
			Description: msg.message,
		}},
	}
}

func buildTelegramText(msg *alertMsg) string {
	prefix := "🚨 ALERT: "
	if msg.resolved {
		prefix = "💜 Resolved: "
	}
	return fmt.Sprintf("%s %s: %s - %s", BrandName, msg.chain, prefix, msg.message)
}

type contextHTTPClient struct {
	ctx    context.Context
	client *http.Client
}

func (c contextHTTPClient) Do(req *http.Request) (*http.Response, error) {
	//#nosec G704 -- production requests are constructed by the Telegram client
	// from its fixed API endpoint; tests replace that package-private endpoint.
	return c.client.Do(req.WithContext(c.ctx))
}

func notifyTg(msg *alertMsg) error {
	return notifications.notifyTelegram(msg)
}

func (s *notificationSender) notifyTelegram(msg *alertMsg) (err error) {
	if !msg.tg {
		return nil
	}
	if !s.cache.reserveDelivery(msg, tg) {
		return nil
	}
	accepted := false
	defer func() { s.cache.completeDelivery(msg, tg, accepted) }()

	ctx, cancel := context.WithTimeout(context.Background(), notificationAttemptTimeout)
	defer cancel()
	client := contextHTTPClient{ctx: ctx, client: s.httpClient}
	bot, err := tgbotapi.NewBotAPIWithClient(msg.tgKey, s.telegramEndpoint, client)
	if err != nil {
		return s.observeError("telegram", classifyTelegramError(err))
	}
	mc := tgbotapi.NewMessageToChannel(msg.tgChannel, buildTelegramText(msg))
	if _, err = bot.Send(mc); err != nil {
		return s.observeError("telegram", classifyTelegramError(err))
	}
	accepted = true
	s.observeAccepted("telegram")
	return nil
}

func classifyTelegramError(err error) error {
	var apiErr *tgbotapi.Error
	if errors.As(err, &apiErr) {
		return &NotificationAPIError{Destination: "telegram", cause: err}
	}
	return &NotificationTransportError{Destination: "telegram", cause: err}
}

func buildPagerDutyEvent(msg *alertMsg) pagerduty.V2Event {
	action := "trigger"
	if msg.resolved {
		action = "resolve"
	}
	return pagerduty.V2Event{
		RoutingKey: msg.key,
		Action:     action,
		DedupKey:   msg.uniqueId,
		Payload: &pagerduty.V2Payload{
			Summary:  fmt.Sprintf("%s: %s", BrandName, msg.message),
			Source:   msg.uniqueId,
			Severity: msg.severity,
		},
	}
}

func notifyPagerduty(msg *alertMsg) error {
	return notifications.notifyPagerDuty(msg)
}

func (s *notificationSender) notifyPagerDuty(msg *alertMsg) (err error) {
	if !msg.pd {
		return nil
	}
	// key from the example, don't spam their API
	if msg.key == "aaaaaaaaaaaabbbbbbbbbbbbbcccccccccccc" {
		l("invalid pagerduty key")
		return nil
	}
	if !s.cache.reserveDelivery(msg, pd) {
		return nil
	}
	accepted := false
	defer func() { s.cache.completeDelivery(msg, pd, accepted) }()

	var lastErr error
	for attempt := 1; attempt <= pagerDutyMaxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), notificationAttemptTimeout)
		client := pagerduty.NewClient("")
		if s.pagerDutyEndpoint != "" {
			client = pagerduty.NewClient("", pagerduty.WithV2EventsAPIEndpoint(s.pagerDutyEndpoint))
		}
		client.HTTPClient = s.httpClient
		_, sendErr := client.ManageEventWithContext(ctx, ptrPagerDutyEvent(buildPagerDutyEvent(msg)))
		cancel()
		if sendErr == nil {
			accepted = true
			s.observeAccepted("pagerduty")
			return nil
		}
		lastErr = s.observeError("pagerduty", classifyPagerDutyError(sendErr))
		if attempt < pagerDutyMaxAttempts {
			s.sleep(pagerDutyRetryDelay)
		}
	}
	return lastErr
}

func ptrPagerDutyEvent(event pagerduty.V2Event) *pagerduty.V2Event {
	return &event
}

func classifyPagerDutyError(err error) error {
	var apiErr pagerduty.APIError
	if errors.As(err, &apiErr) {
		return &NotificationAPIError{Destination: "pagerduty", cause: err}
	}
	return &NotificationTransportError{Destination: "pagerduty", cause: err}
}

func getAlarms(chain string) string {
	alarms.notifyMux.RLock()
	defer alarms.notifyMux.RUnlock()
	// don't show this info if the logs are disabled on the dashboard, potentially sensitive info could be leaked.
	if td.HideLogs || alarms.AllAlarms[chain] == nil {
		return ""
	}
	result := ""
	for k := range alarms.AllAlarms[chain] {
		result += "🚨 " + k + "\n"
	}
	return result
}

// alert creates a universal alert and pushes it to the alertChan to be delivered to appropriate services
func (c *Config) alert(chainName, message, severity string, resolved bool, id *string) {
	c.chainsMux.RLock()
	chain := c.Chains[chainName]
	uniq := chain.ValAddress
	if id != nil {
		uniq = *id
	}
	a := &alertMsg{
		pd:           c.Pagerduty.Enabled && chain.Alerts.Pagerduty.Enabled,
		disc:         c.Discord.Enabled && chain.Alerts.Discord.Enabled,
		tg:           c.Telegram.Enabled && chain.Alerts.Telegram.Enabled,
		slk:          c.Slack.Enabled && chain.Alerts.Slack.Enabled,
		severity:     severity,
		resolved:     resolved,
		chain:        chainName,
		message:      message,
		uniqueId:     uniq,
		key:          chain.Alerts.Pagerduty.ApiKey,
		tgChannel:    chain.Alerts.Telegram.Channel,
		tgKey:        chain.Alerts.Telegram.ApiKey,
		tgMentions:   strings.Join(chain.Alerts.Telegram.Mentions, " "),
		discHook:     chain.Alerts.Discord.Webhook,
		discMentions: strings.Join(chain.Alerts.Discord.Mentions, " "),
		slkHook:      chain.Alerts.Slack.Webhook,
	}
	c.chainsMux.RUnlock()
	c.alertChan <- a
	alarms.notifyMux.Lock()
	defer alarms.notifyMux.Unlock()
	if alarms.AllAlarms[chainName] == nil {
		alarms.AllAlarms[chainName] = make(map[string]time.Time)
	}
	if resolved && !alarms.AllAlarms[chainName][message].IsZero() {
		delete(alarms.AllAlarms[chainName], message)
		return
	} else if resolved {
		return
	}
	alarms.AllAlarms[chainName][message] = time.Now()
}

func (cc *ChainConfig) checkStalledAlert(now time.Time) {
	resolved := false
	cc.alertStateMux.Lock()
	switch {
	case cc.Alerts.StalledAlerts && !cc.lastBlockAlarm && !cc.lastBlockTime.IsZero() &&
		cc.lastBlockTime.Before(now.Add(time.Duration(-cc.Alerts.Stalled)*time.Minute)):
		cc.lastBlockAlarm = true
	case cc.Alerts.StalledAlerts && cc.lastBlockAlarm && cc.stalledResolutionPending:
		cc.lastBlockAlarm = false
		cc.stalledResolutionPending = false
		resolved = true
	default:
		cc.alertStateMux.Unlock()
		return
	}
	cc.alertStateMux.Unlock()

	severity := "critical"
	if resolved {
		severity = "info"
	}
	td.alert(
		cc.name,
		fmt.Sprintf("stalled: have not seen a new block on %s in %d minutes", cc.ChainId, cc.Alerts.Stalled),
		severity,
		resolved,
		&cc.valInfo.Valcons,
	)
	if resolved {
		alarms.clearNoBlocks(cc.name)
	}
}

// recordFinalBlock preserves an active stalled alarm until the watch loop has
// emitted its matching resolution. It returns the previous finalized block time.
func (cc *ChainConfig) recordFinalBlock(now time.Time) time.Time {
	cc.alertStateMux.Lock()
	defer cc.alertStateMux.Unlock()
	previous := cc.lastBlockTime
	cc.lastBlockTime = now
	if cc.lastBlockAlarm {
		cc.stalledResolutionPending = true
	}
	return previous
}

func (cc *ChainConfig) lastFinalBlockTime() time.Time {
	cc.alertStateMux.Lock()
	defer cc.alertStateMux.Unlock()
	return cc.lastBlockTime
}

func (cc *ChainConfig) checkPercentageAlert() {
	cc.alertStateMux.Lock()
	if !cc.Alerts.PercentageAlerts || cc.valInfo == nil || cc.valInfo.Window == 0 {
		cc.alertStateMux.Unlock()
		return
	}
	percentMissed := 100 * float64(cc.valInfo.Missed) / float64(cc.valInfo.Window)
	resolved := false
	switch {
	case !cc.percentageAlarm && percentMissed > float64(cc.Alerts.Window):
		cc.percentageAlarm = true
	case cc.percentageAlarm && percentMissed < float64(cc.Alerts.Window):
		cc.percentageAlarm = false
		resolved = true
	default:
		cc.alertStateMux.Unlock()
		return
	}
	moniker := cc.valInfo.Moniker
	valcons := cc.valInfo.Valcons
	cc.alertStateMux.Unlock()

	severity := cc.Alerts.PercentagePriority
	if resolved {
		severity = "info"
	}
	id := valcons + "percent"
	td.alert(
		cc.name,
		fmt.Sprintf("%s has missed > %d%% of the slashing window's blocks on %s", moniker, cc.Alerts.Window, cc.ChainId),
		severity,
		resolved,
		&id,
	)
	cc.activeAlerts = alarms.getCount(cc.name)
}

// watch handles monitoring for missed blocks, stalled chain, node downtime
// and also updates a few prometheus stats
// FIXME: not watching for nodes that are lagging the head block!
func (cc *ChainConfig) watch() {
	var missedAlarm, noNodes bool
	inactive := "jailed"
	nodeAlarms := make(map[string]bool)

	// wait until we have a moniker:
	noNodesSec := 0 // delay a no-nodes alarm for 30 seconds, too noisy.
	for {
		if cc.valInfo == nil || cc.valInfo.Moniker == "not connected" {
			time.Sleep(time.Second)
			if cc.Alerts.AlertIfNoServers && !noNodes && cc.noNodes && noNodesSec >= 60*td.NodeDownMin {
				noNodes = true
				td.alert(
					cc.name,
					fmt.Sprintf("no RPC endpoints are working for %s", cc.ChainId),
					"critical",
					false,
					&cc.valInfo.Valcons,
				)
			}
			noNodesSec += 1
			continue
		}
		noNodesSec = 0
		break
	}
	// initial stat creation for nodes, we only update again if the node is positive
	if td.Prom {
		for _, node := range cc.Nodes {
			td.statsChan <- cc.mkUpdate(metricNodeDownSeconds, 0, node.Url)
		}
	}

	for {
		time.Sleep(2 * time.Second)

		// alert if we can't monitor
		switch {
		case cc.Alerts.AlertIfNoServers && !noNodes && cc.noNodes:
			noNodesSec += 2
			if noNodesSec <= 30*td.NodeDownMin {
				if noNodesSec%20 == 0 {
					l(fmt.Sprintf("no nodes available on %s for %d seconds, deferring alarm", cc.ChainId, noNodesSec))
				}
				noNodes = false
			} else {
				noNodesSec = 0
				noNodes = true
				td.alert(
					cc.name,
					fmt.Sprintf("no RPC endpoints are working for %s", cc.ChainId),
					"critical",
					false,
					&cc.valInfo.Valcons,
				)
			}
		case cc.Alerts.AlertIfNoServers && noNodes && !cc.noNodes:
			noNodes = false
			td.alert(
				cc.name,
				fmt.Sprintf("no RPC endpoints are working for %s", cc.ChainId),
				"critical",
				true,
				&cc.valInfo.Valcons,
			)
		default:
			noNodesSec = 0
		}

		// stalled chain detection
		cc.checkStalledAlert(time.Now())

		// jailed detection - only alert if it changes.
		if cc.Alerts.AlertIfInactive && cc.lastValInfo != nil && cc.lastValInfo.Bonded != cc.valInfo.Bonded &&
			cc.lastValInfo.Moniker == cc.valInfo.Moniker {

			id := cc.valInfo.Valcons + "jailed"
			// just went inactive, figure out if it's jail or tombstone
			if !cc.valInfo.Bonded && cc.lastValInfo.Bonded {
				if cc.valInfo.Tombstoned {
					// don't worry about changing it back ... lol.
					inactive = "☠️ tombstoned 🪦"
				}
				td.alert(
					cc.name,
					fmt.Sprintf("%s is no longer active: validator is %s", cc.valInfo.Moniker, inactive),
					"critical",
					false,
					&id,
				)
			} else if cc.valInfo.Bonded && !cc.lastValInfo.Bonded {
				td.alert(
					cc.name,
					fmt.Sprintf("%s is no longer active: validator is %s", cc.valInfo.Moniker, inactive),
					"info",
					true,
					&id,
				)
			}
		}

		// consecutive missed block alarms:
		if !missedAlarm && cc.Alerts.ConsecutiveAlerts && int(cc.statConsecutiveMiss) >= cc.Alerts.ConsecutiveMissed {
			// alert on missed block counter!
			missedAlarm = true
			id := cc.valInfo.Valcons + "consecutive"
			td.alert(
				cc.name,
				fmt.Sprintf("%s has missed %d blocks on %s", cc.valInfo.Moniker, cc.Alerts.ConsecutiveMissed, cc.ChainId),
				cc.Alerts.ConsecutivePriority,
				false,
				&id,
			)
			cc.activeAlerts = alarms.getCount(cc.name)
		} else if missedAlarm && int(cc.statConsecutiveMiss) < cc.Alerts.ConsecutiveMissed {
			// clear the alert
			missedAlarm = false
			id := cc.valInfo.Valcons + "consecutive"
			td.alert(
				cc.name,
				fmt.Sprintf("%s has missed %d blocks on %s", cc.valInfo.Moniker, cc.Alerts.ConsecutiveMissed, cc.ChainId),
				"info",
				true,
				&id,
			)
			cc.activeAlerts = alarms.getCount(cc.name)
		}

		// window percentage missed block alarms
		cc.checkPercentageAlert()

		// node down alarms
		for _, node := range cc.Nodes {
			// window percentage missed block alarms
			if node.AlertIfDown && node.down && !node.wasDown && !node.downSince.IsZero() &&
				time.Since(node.downSince) > time.Duration(td.NodeDownMin)*time.Minute {
				// alert on dead node
				if !nodeAlarms[node.Url] {
					cc.activeAlerts = alarms.getCount(cc.name)
				} else {
					continue
				}
				nodeAlarms[node.Url] = true // used to keep active alert count correct
				td.alert(
					cc.name,
					fmt.Sprintf("Severity: %s\nRPC node %s has been down for > %d minutes on %s", td.NodeDownSeverity, node.Url, td.NodeDownMin, cc.ChainId),
					td.NodeDownSeverity,
					false,
					&node.Url,
				)
			} else if node.AlertIfDown && !node.down && node.wasDown {
				// clear the alert
				nodeAlarms[node.Url] = false
				node.wasDown = false
				td.alert(
					cc.name,
					fmt.Sprintf("Severity: %s\nRPC node %s has been down for > %d minutes on %s", td.NodeDownSeverity, node.Url, td.NodeDownMin, cc.ChainId),
					"info",
					true,
					&node.Url,
				)
				cc.activeAlerts = alarms.getCount(cc.name)
			}
		}

		if td.Prom {
			// raw block timer, ignoring finalized state
			td.statsChan <- cc.mkUpdate(metricLastBlockSecondsNotFinal, time.Since(cc.lastFinalBlockTime()).Seconds(), "")
			// update node-down times for prometheus
			for _, node := range cc.Nodes {
				if node.down && !node.downSince.IsZero() {
					td.statsChan <- cc.mkUpdate(metricNodeDownSeconds, time.Since(node.downSince).Seconds(), node.Url)
				}
			}
		}
	}
}
