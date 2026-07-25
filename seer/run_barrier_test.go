package seer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestShutdownDrainsAcceptedDeliveryBeforeSingleCheckpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cache := newAlarmCache()
	chain := &ChainConfig{blocksResults: []int{1}}
	config := &Config{
		alertChan: make(chan *alertMsg, 1),
		ctx:       ctx,
		cancel:    cancel,
		alarms:    cache,
		Chains:    map[string]*ChainConfig{"barrier": chain},
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	var checkpoints atomic.Int32
	lifecycle := newRuntimeLifecycle(config, statePath, time.Second, func(path string, state *savedState) error {
		checkpoints.Add(1)
		return writeStateAtomic(path, state)
	})

	deliveryStarted := make(chan struct{})
	releaseDelivery := make(chan struct{})
	lifecycle.startNotificationWorker(func(msg *alertMsg) {
		close(deliveryStarted)
		<-releaseDelivery
		if cache.reserveDelivery(msg, slk) {
			cache.completeDelivery(msg, slk, true)
		}
	})
	monitorStopped := make(chan struct{})
	lifecycle.startMonitor(func(ctx context.Context) {
		<-ctx.Done()
		chain.recordBlockResult(77)
		close(monitorStopped)
	})

	msg := &alertMsg{message: "accepted-at-shutdown"}
	if !config.enqueueAlert(msg) {
		t.Fatal("alert ingress rejected before shutdown")
	}
	<-deliveryStarted

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- lifecycle.shutdown() }()
	<-monitorStopped
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint happened before delivery drain: %v", err)
	}
	if config.enqueueAlert(&alertMsg{message: "late-ingress"}) {
		t.Fatal("shutdown accepted new alert ingress")
	}
	close(releaseDelivery)

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown barrier failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown barrier did not finish")
	}
	if got := checkpoints.Load(); got != 1 {
		t.Fatalf("checkpoint count = %d, want exactly 1", got)
	}
	if err := lifecycle.shutdown(); err != nil {
		t.Fatalf("idempotent shutdown: %v", err)
	}
	if got := checkpoints.Load(); got != 1 {
		t.Fatalf("checkpoint count after repeated shutdown = %d, want exactly 1", got)
	}

	saved, _, err := loadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Alarms.SentSlkAlarms[msg.message].IsZero() {
		t.Fatal("accepted delivery completed during shutdown but was absent from checkpoint")
	}
	if got := saved.Blocks["barrier"][0]; got != 77 {
		t.Fatalf("monitor mutation during cancellation = %d, want 77", got)
	}
}

func TestCloseWebSocketsUnblocksRead(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverRelease := make(chan struct{})
	serverReady := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		close(serverReady)
		<-serverRelease
	}))
	defer server.Close()
	defer close(serverRelease)

	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	<-serverReady
	chain := &ChainConfig{wsclient: &TmConn{Conn: connection}}
	config := &Config{Chains: map[string]*ChainConfig{"websocket": chain}}
	readDone := make(chan error, 1)
	go func() {
		_, _, readErr := connection.ReadMessage()
		readDone <- readErr
	}()

	config.closeWebSockets()
	select {
	case readErr := <-readDone:
		if readErr == nil {
			t.Fatal("closed websocket read returned nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("closing runtime websockets did not unblock ReadMessage")
	}
}

func TestShutdownDrainTimeoutSkipsCheckpointAndFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	config := &Config{
		alertChan: make(chan *alertMsg, 1),
		ctx:       ctx,
		cancel:    cancel,
		alarms:    newAlarmCache(),
		Chains:    map[string]*ChainConfig{"blocked": {blocksResults: []int{1}}},
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	var checkpoints atomic.Int32
	lifecycle := newRuntimeLifecycle(config, statePath, 25*time.Millisecond, func(string, *savedState) error {
		checkpoints.Add(1)
		return nil
	})
	release := make(chan struct{})
	lifecycle.startMonitor(func(context.Context) { <-release })

	err := lifecycle.shutdown()
	close(release)
	if err == nil || !errors.Is(err, errShutdownDrainTimeout) {
		t.Fatalf("shutdown error = %v, want bounded drain timeout", err)
	}
	if got := checkpoints.Load(); got != 0 {
		t.Fatalf("checkpoint calls after failed drain = %d, want 0", got)
	}
	if _, statErr := os.Stat(statePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed drain claimed a checkpoint: %v", statErr)
	}
}
