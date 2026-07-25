package seer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPercentageAlertTriggersThenResolves(t *testing.T) {
	cc, alertCh := setupTransitionTest(t)
	cc.Alerts.PercentageAlerts = true
	cc.Alerts.Window = 10
	cc.Alerts.PercentagePriority = "warning"
	cc.valInfo.Missed = 11
	cc.valInfo.Window = 100

	cc.checkPercentageAlert()
	trigger := receiveAlert(t, alertCh)
	if trigger.resolved {
		t.Fatal("percentage trigger was emitted as a resolution")
	}
	if trigger.uniqueId != "consensus-addresspercent" {
		t.Fatalf("percentage trigger dedup id = %q", trigger.uniqueId)
	}

	cc.valInfo.Missed = 9
	cc.checkPercentageAlert()
	resolution := receiveAlert(t, alertCh)
	if !resolution.resolved {
		t.Fatal("percentage recovery was emitted as another trigger")
	}
	if resolution.message != trigger.message || resolution.uniqueId != trigger.uniqueId {
		t.Fatalf("resolution identity changed: trigger=%+v resolution=%+v", trigger, resolution)
	}

	cc.checkPercentageAlert()
	assertNoAlert(t, alertCh)
}

func TestStalledAlertResolvesExactlyOnceAfterNewBlock(t *testing.T) {
	cc, alertCh := setupTransitionTest(t)
	cc.Alerts.StalledAlerts = true
	cc.Alerts.Stalled = 10
	now := time.Unix(1_700_000_000, 0)
	cc.lastBlockTime = now.Add(-11 * time.Minute)

	cc.checkStalledAlert(now)
	trigger := receiveAlert(t, alertCh)
	if trigger.resolved {
		t.Fatal("stalled trigger was emitted as a resolution")
	}

	cc.recordFinalBlock(now)
	if got := cc.lastFinalBlockTime(); !got.Equal(now) {
		t.Fatalf("recorded final block time = %v, want %v", got, now)
	}
	cc.checkStalledAlert(now)
	resolution := receiveAlert(t, alertCh)
	if !resolution.resolved {
		t.Fatal("first block after a stall did not resolve the alert")
	}
	if resolution.message != trigger.message || resolution.uniqueId != trigger.uniqueId {
		t.Fatalf("resolution identity changed: trigger=%+v resolution=%+v", trigger, resolution)
	}

	cc.checkStalledAlert(now)
	assertNoAlert(t, alertCh)
}

func TestSlackFailureRemainsRetryable(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "rejected", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cache := newAlarmCache()
	sender := newNotificationSender(cache, server.Client())
	msg := &alertMsg{slk: true, chain: "secret-chain", message: "secret payload", slkHook: server.URL}

	err := sender.notifySlack(msg)
	var statusErr *NotificationHTTPError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("first attempt error = %v, want typed 503 rejection", err)
	}
	if !cache.SentSlkAlarms[msg.message].IsZero() {
		t.Fatal("failed Slack delivery was committed as sent")
	}

	if err := sender.notifySlack(msg); err != nil {
		t.Fatalf("retry after explicit rejection failed: %v", err)
	}
	if cache.SentSlkAlarms[msg.message].IsZero() {
		t.Fatal("accepted Slack delivery was not committed")
	}

	if err := sender.notifySlack(msg); err != nil {
		t.Fatalf("duplicate Slack delivery returned error: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("Slack request count = %d, want 2 (rejection + retry; duplicate suppressed)", got)
	}
}

func TestSlackAcceptedResolutionClearsAcceptedAlert(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cache := newAlarmCache()
	sender := newNotificationSender(cache, server.Client())
	trigger := &alertMsg{slk: true, message: "missed blocks", slkHook: server.URL}
	if err := sender.notifySlack(trigger); err != nil {
		t.Fatalf("trigger failed: %v", err)
	}
	resolution := *trigger
	resolution.resolved = true
	if err := sender.notifySlack(&resolution); err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if !cache.SentSlkAlarms[trigger.message].IsZero() {
		t.Fatal("accepted Slack resolution did not clear accepted alert state")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("Slack request count = %d, want trigger + resolution", got)
	}
}

func TestSlackResolutionWithoutAcceptedAlertIsSuppressed(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cache := newAlarmCache()
	sender := newNotificationSender(cache, server.Client())
	resolution := &alertMsg{slk: true, resolved: true, message: "never delivered", slkHook: server.URL}
	if err := sender.notifySlack(resolution); err != nil {
		t.Fatalf("suppressed resolution returned error: %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("resolution without accepted trigger made %d requests", got)
	}
}

func TestSlackTransportFailureIsSanitizedAndRetryable(t *testing.T) {
	const secret = "https://hooks.example.invalid/services/secret-token"
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return nil, fmt.Errorf("dial %s", secret)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})}
	cache := newAlarmCache()
	sender := newNotificationSender(cache, client)
	msg := &alertMsg{slk: true, message: "payload secret", slkHook: secret}

	err := sender.notifySlack(msg)
	var transportErr *NotificationTransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("transport error type = %T, want *NotificationTransportError", err)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), msg.message) {
		t.Fatalf("transport error leaked notification secret: %q", err)
	}
	if !cache.SentSlkAlarms[msg.message].IsZero() {
		t.Fatal("transport failure was committed as sent")
	}
	if err := sender.notifySlack(msg); err != nil {
		t.Fatalf("next-cycle retry failed: %v", err)
	}
}

func TestSlackConcurrentCallsSendOnce(t *testing.T) {
	const workers = 32
	started := make(chan struct{}, workers)
	release := make(chan struct{})
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-release
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})}
	cache := newAlarmCache()
	sender := newNotificationSender(cache, client)
	msg := &alertMsg{slk: true, message: "one incident", slkHook: "https://example.invalid/hook"}

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- sender.notifySlack(msg)
		}()
	}
	<-started
	duplicateStarted := false
	select {
	case <-started:
		duplicateStarted = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Slack delivery failed: %v", err)
		}
	}
	if duplicateStarted {
		t.Fatal("concurrent calls started more than one Slack request")
	}
}

func TestDiscordDeliveryLifecycle(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cache := newAlarmCache()
	sender := newNotificationSender(cache, server.Client())
	trigger := &alertMsg{disc: true, message: "discord incident", discHook: server.URL}
	var statusErr *NotificationHTTPError
	if err := sender.notifyDiscord(trigger); !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("Discord rejection error = %v, want typed 429", err)
	}
	if !cache.SentDiAlarms[trigger.message].IsZero() {
		t.Fatal("rejected Discord trigger was committed")
	}
	if err := sender.notifyDiscord(trigger); err != nil {
		t.Fatalf("Discord retry failed: %v", err)
	}
	if err := sender.notifyDiscord(trigger); err != nil {
		t.Fatalf("Discord duplicate returned error: %v", err)
	}
	resolution := *trigger
	resolution.resolved = true
	if err := sender.notifyDiscord(&resolution); err != nil {
		t.Fatalf("Discord resolution failed: %v", err)
	}
	if !cache.SentDiAlarms[trigger.message].IsZero() {
		t.Fatal("accepted Discord resolution did not clear alert state")
	}
	orphan := &alertMsg{disc: true, resolved: true, message: "never accepted", discHook: server.URL}
	if err := sender.notifyDiscord(orphan); err != nil {
		t.Fatalf("orphan Discord resolution returned error: %v", err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("Discord request count = %d, want reject + accepted trigger + accepted resolution", got)
	}
}

func TestTelegramDeliveryLifecycle(t *testing.T) {
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Seer","username":"seer_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			if sends.Add(1) == 1 {
				_, _ = io.WriteString(w, `{"ok":false,"error_code":500,"description":"rejected"}`)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"channel"},"text":"ok"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cache := newAlarmCache()
	sender := newNotificationSender(cache, server.Client())
	sender.telegramEndpoint = server.URL + "/bot%s/%s"
	trigger := &alertMsg{tg: true, tgKey: "secret", tgChannel: "channel", message: "telegram incident"}
	var apiErr *NotificationAPIError
	if err := sender.notifyTelegram(trigger); !errors.As(err, &apiErr) {
		t.Fatalf("Telegram rejection error = %v, want typed API rejection", err)
	}
	if !cache.SentTgAlarms[trigger.message].IsZero() {
		t.Fatal("rejected Telegram trigger was committed")
	}
	if err := sender.notifyTelegram(trigger); err != nil {
		t.Fatalf("Telegram retry failed: %v", err)
	}
	if err := sender.notifyTelegram(trigger); err != nil {
		t.Fatalf("Telegram duplicate returned error: %v", err)
	}
	resolution := *trigger
	resolution.resolved = true
	if err := sender.notifyTelegram(&resolution); err != nil {
		t.Fatalf("Telegram resolution failed: %v", err)
	}
	if !cache.SentTgAlarms[trigger.message].IsZero() {
		t.Fatal("accepted Telegram resolution did not clear alert state")
	}
	orphan := &alertMsg{tg: true, resolved: true, tgKey: "secret", tgChannel: "channel", message: "never accepted"}
	if err := sender.notifyTelegram(orphan); err != nil {
		t.Fatalf("orphan Telegram resolution returned error: %v", err)
	}
	if got := sends.Load(); got != 3 {
		t.Fatalf("Telegram send count = %d, want reject + accepted trigger + accepted resolution", got)
	}
}

func TestPagerDutyDeliveryLifecycleAndBoundedRetry(t *testing.T) {
	var requests atomic.Int32
	var sleeps atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event struct {
			Action   string `json:"event_action"`
			DedupKey string `json:"dedup_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Errorf("decode PagerDuty event: %v", err)
		}
		if event.DedupKey != "stable-dedup-key" {
			t.Errorf("PagerDuty dedup key = %q, want stable-dedup-key", event.DedupKey)
		}
		n := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n <= 2 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"rejected"}}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, fmt.Sprintf(`{"status":"success","dedup_key":%q}`, event.DedupKey))
	}))
	defer server.Close()

	cache := newAlarmCache()
	sender := newNotificationSender(cache, server.Client())
	sender.pagerDutyEndpoint = server.URL
	sender.sleep = func(delay time.Duration) {
		if delay != pagerDutyRetryDelay {
			t.Errorf("retry delay = %v, want %v", delay, pagerDutyRetryDelay)
		}
		sleeps.Add(1)
	}
	trigger := &alertMsg{pd: true, key: "routing-key", uniqueId: "stable-dedup-key", chain: "chain", severity: "critical", message: "pagerduty incident"}

	var apiErr *NotificationAPIError
	if err := sender.notifyPagerDuty(trigger); !errors.As(err, &apiErr) {
		t.Fatalf("PagerDuty rejection error = %v, want typed API rejection", err)
	}
	if got := requests.Load(); got != pagerDutyMaxAttempts {
		t.Fatalf("PagerDuty attempts = %d, want bounded %d", got, pagerDutyMaxAttempts)
	}
	if !cache.SentPdAlarms[trigger.message].IsZero() {
		t.Fatal("rejected PagerDuty trigger was committed")
	}
	if err := sender.notifyPagerDuty(trigger); err != nil {
		t.Fatalf("PagerDuty next-cycle retry failed: %v", err)
	}
	if err := sender.notifyPagerDuty(trigger); err != nil {
		t.Fatalf("PagerDuty duplicate returned error: %v", err)
	}
	resolution := *trigger
	resolution.resolved = true
	if err := sender.notifyPagerDuty(&resolution); err != nil {
		t.Fatalf("PagerDuty resolution failed: %v", err)
	}
	if !cache.SentPdAlarms[trigger.message].IsZero() {
		t.Fatal("accepted PagerDuty resolution did not clear alert state")
	}
	orphan := &alertMsg{pd: true, resolved: true, key: "routing-key", uniqueId: "orphan-dedup", message: "never accepted"}
	if err := sender.notifyPagerDuty(orphan); err != nil {
		t.Fatalf("orphan PagerDuty resolution returned error: %v", err)
	}
	if got := requests.Load(); got != 4 {
		t.Fatalf("PagerDuty request count = %d, want two rejects + trigger + resolution", got)
	}
	if got := sleeps.Load(); got != 1 {
		t.Fatalf("PagerDuty retry backoffs = %d, want 1", got)
	}
}

func TestDeliveryOutcomeObserverUsesBoundedLabels(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	type outcome struct{ destination, result string }
	var outcomes []outcome
	sender := newNotificationSender(newAlarmCache(), server.Client())
	sender.observe = func(destination, result string) {
		outcomes = append(outcomes, outcome{destination: destination, result: result})
	}
	msg := &alertMsg{slk: true, slkHook: server.URL, chain: "private-chain", message: "private payload"}
	_ = sender.notifySlack(msg)
	_ = sender.notifySlack(msg)

	want := []outcome{{destination: "slack", result: "rejected"}, {destination: "slack", result: "accepted"}}
	if fmt.Sprint(outcomes) != fmt.Sprint(want) {
		t.Fatalf("delivery outcomes = %v, want %v", outcomes, want)
	}
	for _, got := range outcomes {
		if strings.Contains(got.destination, msg.chain) || strings.Contains(got.result, msg.message) {
			t.Fatalf("delivery outcome labels leaked private data: %+v", got)
		}
	}
}

func TestNotificationWorkerIsSerialAndOrdered(t *testing.T) {
	alerts := make(chan *alertMsg, 2)
	done := make(chan struct{})
	var mu sync.Mutex
	active, maxActive := 0, 0
	var order []string
	deliver := func(msg *alertMsg) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		order = append(order, msg.message)
		active--
		if len(order) == 2 {
			close(done)
		}
		mu.Unlock()
	}
	go notificationWorker(alerts, deliver)
	alerts <- &alertMsg{message: "trigger"}
	alerts <- &alertMsg{message: "resolution", resolved: true}
	close(alerts)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("notification worker did not drain bounded test queue")
	}
	mu.Lock()
	defer mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("maximum concurrent deliveries = %d, want 1", maxActive)
	}
	if got := strings.Join(order, ","); got != "trigger,resolution" {
		t.Fatalf("delivery order = %q, want trigger,resolution", got)
	}
}

func TestTransportFailureRemainsNextCycleEligibleForEverySink(t *testing.T) {
	tests := []struct {
		name                string
		failuresBeforeReady int32
		message             *alertMsg
		configure           func(*notificationSender, string)
		send                func(*notificationSender, *alertMsg) error
		sent                func(*alarmCache, string) time.Time
	}{
		{
			name: "slack", failuresBeforeReady: 1,
			message: &alertMsg{slk: true, slkHook: "https://hooks.invalid/secret", message: "slack transport"},
			send:    func(s *notificationSender, m *alertMsg) error { return s.notifySlack(m) },
			sent:    func(c *alarmCache, key string) time.Time { return c.SentSlkAlarms[key] },
		},
		{
			name: "discord", failuresBeforeReady: 1,
			message: &alertMsg{disc: true, discHook: "https://discord.invalid/secret", message: "discord transport"},
			send:    func(s *notificationSender, m *alertMsg) error { return s.notifyDiscord(m) },
			sent:    func(c *alarmCache, key string) time.Time { return c.SentDiAlarms[key] },
		},
		{
			name: "telegram", failuresBeforeReady: 1,
			message:   &alertMsg{tg: true, tgKey: "secret-token", tgChannel: "private-channel", message: "telegram transport"},
			configure: func(s *notificationSender, endpoint string) { s.telegramEndpoint = endpoint + "/bot%s/%s" },
			send:      func(s *notificationSender, m *alertMsg) error { return s.notifyTelegram(m) },
			sent:      func(c *alarmCache, key string) time.Time { return c.SentTgAlarms[key] },
		},
		{
			name: "pagerduty", failuresBeforeReady: pagerDutyMaxAttempts,
			message:   &alertMsg{pd: true, key: "routing-key", uniqueId: "transport-dedup", severity: "critical", message: "pagerduty transport"},
			configure: func(s *notificationSender, endpoint string) { s.pagerDutyEndpoint = endpoint },
			send:      func(s *notificationSender, m *alertMsg) error { return s.notifyPagerDuty(m) },
			sent:      func(c *alarmCache, key string) time.Time { return c.SentPdAlarms[key] },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if attempts.Add(1) <= tc.failuresBeforeReady {
					return nil, fmt.Errorf("dial secret endpoint %s", req.URL)
				}
				return successfulSinkResponse(t, tc.name, req), nil
			})}
			cache := newAlarmCache()
			sender := newNotificationSender(cache, client)
			sender.sleep = func(time.Duration) {}
			sender.observe = func(string, string) {}
			if tc.configure != nil {
				tc.configure(sender, "https://sink.invalid")
			}

			err := tc.send(sender, tc.message)
			var transportErr *NotificationTransportError
			if !errors.As(err, &transportErr) {
				t.Fatalf("first error = %v, want typed transport error", err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), tc.message.message) {
				t.Fatalf("transport error leaked secret data: %q", err)
			}
			if !tc.sent(cache, tc.message.message).IsZero() {
				t.Fatal("transport failure was committed as sent")
			}
			if err := tc.send(sender, tc.message); err != nil {
				t.Fatalf("next-cycle retry failed: %v", err)
			}
			if tc.sent(cache, tc.message.message).IsZero() {
				t.Fatal("accepted next-cycle retry was not committed")
			}
		})
	}
}

func TestConcurrentCallsAreSuppressedForEverySink(t *testing.T) {
	const workers = 16
	tests := []struct {
		name      string
		message   *alertMsg
		configure func(*notificationSender, string)
		send      func(*notificationSender, *alertMsg) error
		wantCalls int32
	}{
		{name: "slack", message: &alertMsg{slk: true, slkHook: "https://sink.invalid", message: "slack concurrent"}, send: func(s *notificationSender, m *alertMsg) error { return s.notifySlack(m) }, wantCalls: 1},
		{name: "discord", message: &alertMsg{disc: true, discHook: "https://sink.invalid", message: "discord concurrent"}, send: func(s *notificationSender, m *alertMsg) error { return s.notifyDiscord(m) }, wantCalls: 1},
		{name: "telegram", message: &alertMsg{tg: true, tgKey: "token", tgChannel: "channel", message: "telegram concurrent"}, configure: func(s *notificationSender, endpoint string) { s.telegramEndpoint = endpoint + "/bot%s/%s" }, send: func(s *notificationSender, m *alertMsg) error { return s.notifyTelegram(m) }, wantCalls: 2},
		{name: "pagerduty", message: &alertMsg{pd: true, key: "key", uniqueId: "concurrent-dedup", severity: "critical", message: "pagerduty concurrent"}, configure: func(s *notificationSender, endpoint string) { s.pagerDutyEndpoint = endpoint }, send: func(s *notificationSender, m *alertMsg) error { return s.notifyPagerDuty(m) }, wantCalls: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			started := make(chan struct{}, workers+1)
			release := make(chan struct{})
			var calls atomic.Int32
			client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				started <- struct{}{}
				<-release
				return successfulSinkResponse(t, tc.name, req), nil
			})}
			sender := newNotificationSender(newAlarmCache(), client)
			sender.sleep = func(time.Duration) {}
			sender.observe = func(string, string) {}
			if tc.configure != nil {
				tc.configure(sender, "https://sink.invalid")
			}

			var wg sync.WaitGroup
			errs := make(chan error, workers)
			for range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- tc.send(sender, tc.message)
				}()
			}
			<-started
			duplicateStarted := false
			select {
			case <-started:
				duplicateStarted = true
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("concurrent delivery failed: %v", err)
				}
			}
			if duplicateStarted {
				t.Fatal("concurrent duplicate started before the reserved request completed")
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("network calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}

func TestFailedResolutionRemainsRetryableForEverySink(t *testing.T) {
	tests := []struct {
		name      string
		message   *alertMsg
		configure func(*notificationSender, string)
		send      func(*notificationSender, *alertMsg) error
		seed      func(*alarmCache, string)
		isOpen    func(*alarmCache, string) bool
	}{
		{name: "slack", message: &alertMsg{slk: true, slkHook: "https://sink.invalid", resolved: true, message: "slack resolution"}, send: func(s *notificationSender, m *alertMsg) error { return s.notifySlack(m) }, seed: func(c *alarmCache, key string) { c.SentSlkAlarms[key] = time.Now() }, isOpen: func(c *alarmCache, key string) bool { return !c.SentSlkAlarms[key].IsZero() }},
		{name: "discord", message: &alertMsg{disc: true, discHook: "https://sink.invalid", resolved: true, message: "discord resolution"}, send: func(s *notificationSender, m *alertMsg) error { return s.notifyDiscord(m) }, seed: func(c *alarmCache, key string) { c.SentDiAlarms[key] = time.Now() }, isOpen: func(c *alarmCache, key string) bool { return !c.SentDiAlarms[key].IsZero() }},
		{name: "telegram", message: &alertMsg{tg: true, tgKey: "token", tgChannel: "channel", resolved: true, message: "telegram resolution"}, configure: func(s *notificationSender, endpoint string) { s.telegramEndpoint = endpoint + "/bot%s/%s" }, send: func(s *notificationSender, m *alertMsg) error { return s.notifyTelegram(m) }, seed: func(c *alarmCache, key string) { c.SentTgAlarms[key] = time.Now() }, isOpen: func(c *alarmCache, key string) bool { return !c.SentTgAlarms[key].IsZero() }},
		{name: "pagerduty", message: &alertMsg{pd: true, key: "key", uniqueId: "resolution-dedup", severity: "info", resolved: true, message: "pagerduty resolution"}, configure: func(s *notificationSender, endpoint string) { s.pagerDutyEndpoint = endpoint }, send: func(s *notificationSender, m *alertMsg) error { return s.notifyPagerDuty(m) }, seed: func(c *alarmCache, key string) { c.SentPdAlarms[key] = time.Now() }, isOpen: func(c *alarmCache, key string) bool { return !c.SentPdAlarms[key].IsZero() }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var deliveries atomic.Int32
			client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if tc.name == "telegram" && strings.HasSuffix(req.URL.Path, "/getMe") {
					return successfulSinkResponse(t, tc.name, req), nil
				}
				n := deliveries.Add(1)
				failureLimit := int32(1)
				if tc.name == "pagerduty" {
					failureLimit = pagerDutyMaxAttempts
				}
				if n <= failureLimit {
					status := http.StatusServiceUnavailable
					body := `{"error":{"message":"rejected"}}`
					if tc.name == "telegram" {
						status = http.StatusOK
						body = `{"ok":false,"error_code":500,"description":"rejected"}`
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}, Request: req}, nil
				}
				return successfulSinkResponse(t, tc.name, req), nil
			})}
			cache := newAlarmCache()
			tc.seed(cache, tc.message.message)
			sender := newNotificationSender(cache, client)
			sender.sleep = func(time.Duration) {}
			sender.observe = func(string, string) {}
			if tc.configure != nil {
				tc.configure(sender, "https://sink.invalid")
			}

			if err := tc.send(sender, tc.message); err == nil {
				t.Fatal("rejected resolution returned nil error")
			}
			if !tc.isOpen(cache, tc.message.message) {
				t.Fatal("failed resolution cleared accepted alert state")
			}
			if err := tc.send(sender, tc.message); err != nil {
				t.Fatalf("resolution retry failed: %v", err)
			}
			if tc.isOpen(cache, tc.message.message) {
				t.Fatal("accepted resolution retry did not clear alert state")
			}
		})
	}
}

func TestAlarmCacheMarshalIsRaceSafeDuringDelivery(t *testing.T) {
	cache := newAlarmCache()
	trigger := &alertMsg{message: "marshal-race"}
	resolution := &alertMsg{message: trigger.message, resolved: true}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				if cache.reserveDelivery(trigger, slk) {
					cache.completeDelivery(trigger, slk, true)
				}
				if cache.reserveDelivery(resolution, slk) {
					cache.completeDelivery(resolution, slk, true)
				}
			}
		}
	}()
	for range 100 {
		if _, err := json.Marshal(cache); err != nil {
			close(stop)
			<-done
			t.Fatalf("marshal alarm cache: %v", err)
		}
	}
	close(stop)
	<-done
}

func TestEverySinkAttemptHasBoundedDeadline(t *testing.T) {
	tests := []struct {
		name      string
		message   *alertMsg
		configure func(*notificationSender, string)
		send      func(*notificationSender, *alertMsg) error
	}{
		{name: "slack", message: &alertMsg{slk: true, slkHook: "https://sink.invalid", message: "deadline slack"}, send: func(s *notificationSender, m *alertMsg) error { return s.notifySlack(m) }},
		{name: "discord", message: &alertMsg{disc: true, discHook: "https://sink.invalid", message: "deadline discord"}, send: func(s *notificationSender, m *alertMsg) error { return s.notifyDiscord(m) }},
		{name: "telegram", message: &alertMsg{tg: true, tgKey: "token", tgChannel: "channel", message: "deadline telegram"}, configure: func(s *notificationSender, endpoint string) { s.telegramEndpoint = endpoint + "/bot%s/%s" }, send: func(s *notificationSender, m *alertMsg) error { return s.notifyTelegram(m) }},
		{name: "pagerduty", message: &alertMsg{pd: true, key: "key", uniqueId: "deadline-dedup", severity: "critical", message: "deadline pagerduty"}, configure: func(s *notificationSender, endpoint string) { s.pagerDutyEndpoint = endpoint }, send: func(s *notificationSender, m *alertMsg) error { return s.notifyPagerDuty(m) }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				if !ok {
					t.Error("request has no context deadline")
				} else if remaining := time.Until(deadline); remaining <= 0 || remaining > notificationAttemptTimeout {
					t.Errorf("request deadline remaining = %v, want (0, %v]", remaining, notificationAttemptTimeout)
				}
				return successfulSinkResponse(t, tc.name, req), nil
			})}
			sender := newNotificationSender(newAlarmCache(), client)
			sender.sleep = func(time.Duration) {}
			sender.observe = func(string, string) {}
			if tc.configure != nil {
				tc.configure(sender, "https://sink.invalid")
			}
			if err := tc.send(sender, tc.message); err != nil {
				t.Fatalf("delivery failed: %v", err)
			}
		})
	}
}

func successfulSinkResponse(t *testing.T, destination string, req *http.Request) *http.Response {
	t.Helper()
	status := http.StatusOK
	body := "ok"
	switch destination {
	case "slack":
	case "discord":
		status = http.StatusNoContent
		body = ""
	case "telegram":
		body = `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Seer","username":"seer_bot"}}`
		if strings.HasSuffix(req.URL.Path, "/sendMessage") {
			body = `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"channel"},"text":"ok"}}`
		}
	case "pagerduty":
		status = http.StatusAccepted
		body = `{"status":"success","dedup_key":"dedup"}`
	default:
		t.Fatalf("unknown destination %q", destination)
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func setupTransitionTest(t *testing.T) (*ChainConfig, chan *alertMsg) {
	t.Helper()
	oldTD, oldAlarms := td, alarms
	alarmState := &alarmCache{
		SentPdAlarms:   make(map[string]time.Time),
		SentTgAlarms:   make(map[string]time.Time),
		SentDiAlarms:   make(map[string]time.Time),
		SentSlkAlarms:  make(map[string]time.Time),
		AllAlarms:      make(map[string]map[string]time.Time),
		flappingAlarms: make(map[string]map[string]time.Time),
	}
	alarms = alarmState
	alertCh := make(chan *alertMsg, 8)
	cc := &ChainConfig{
		name:       "test-chain",
		ChainId:    "test-1",
		ValAddress: "validator-address",
		valInfo: &ValInfo{
			Moniker: "validator",
			Valcons: "consensus-address",
		},
	}
	td = &Config{
		alertChan: alertCh,
		Chains:    map[string]*ChainConfig{"test-chain": cc},
	}
	td.startAlertIngress()
	t.Cleanup(func() {
		td = oldTD
		alarms = oldAlarms
	})
	return cc, alertCh
}

func receiveAlert(t *testing.T, alertCh <-chan *alertMsg) *alertMsg {
	t.Helper()
	select {
	case msg := <-alertCh:
		return msg
	default:
		t.Fatal("expected an alert event")
		return nil
	}
}

func assertNoAlert(t *testing.T, alertCh <-chan *alertMsg) {
	t.Helper()
	select {
	case msg := <-alertCh:
		t.Fatalf("unexpected extra alert: %+v", msg)
	default:
	}
}
