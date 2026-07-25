package seer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	dash "github.com/n0sn0de/tenderduty-nos/seer/dashboard"
)

const listenerTestTimeout = time.Second

func reserveKernelPorts(host string, count int) ([]int, error) {
	listeners := make([]net.Listener, 0, count)
	ports := make([]int, 0, count)
	for range count {
		listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			for _, openListener := range listeners {
				_ = openListener.Close()
			}
			return nil, err
		}
		listeners = append(listeners, listener)
		ports = append(ports, listener.Addr().(*net.TCPAddr).Port)
	}
	var closeErr error
	for _, listener := range listeners {
		closeErr = errors.Join(closeErr, listener.Close())
	}
	return ports, closeErr
}

func kernelPorts(t *testing.T, host string, count int) []int {
	t.Helper()
	ports, err := reserveKernelPorts(host, count)
	if err != nil {
		t.Fatalf("reserve %d kernel ports on %q: %v", count, host, err)
	}
	return ports

}

func kernelPort(t *testing.T, host string) int {
	t.Helper()
	return kernelPorts(t, host, 1)[0]
}

func listenerTestConfig(dashboardHost string, dashboardPort int, prometheusHost string, prometheusPort int) *Config {
	return &Config{
		EnableDash:           true,
		ListenHost:           dashboardHost,
		Listen:               strconv.Itoa(dashboardPort),
		Prom:                 true,
		PrometheusListenHost: prometheusHost,
		PrometheusListenPort: prometheusPort,
		HideLogs:             true,
		Chains:               map[string]*ChainConfig{},
		alertChan:            make(chan *alertMsg, 1),
		updateChan:           make(chan *dash.ChainStatus, 1),
		logChan:              make(chan dash.LogMessage, 1),
		statsChan:            make(chan *promUpdate, 1),
		alarms:               newAlarmCache(),
	}
}

func listenerURL(address, path string) string {
	return (&url.URL{Scheme: "http", Host: address, Path: path}).String()
}

func waitForHTTP(t *testing.T, endpoint string) {
	t.Helper()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(listenerTestTimeout)
	for time.Now().Before(deadline) {
		response, err := client.Get(endpoint)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("listener did not become ready at %s", endpoint)
}

func waitForHTTPBody(t *testing.T, endpoint, want string) {
	t.Helper()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(listenerTestTimeout)
	for time.Now().Before(deadline) {
		response, err := client.Get(endpoint)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr == nil && strings.Contains(string(body), want) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("listener response at %s did not contain %q", endpoint, want)
}

func assertCannotConnect(t *testing.T, address string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatalf("listener still accepted a connection at %s", address)
	}
}

func assertCanRebind(t *testing.T, address string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("listener address %s could not be rebound: %v", address, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close rebound listener %s: %v", address, err)
	}
}

func TestListenerAddressesPreserveWildcardAndJoinExplicitHosts(t *testing.T) {
	tests := []struct {
		name           string
		dashboardHost  string
		prometheusHost string
		wantDashboard  string
		wantPrometheus string
	}{
		{name: "omitted hosts preserve wildcard", wantDashboard: ":8888", wantPrometheus: ":28686"},
		{name: "IPv4 loopback", dashboardHost: "127.0.0.1", prometheusHost: "127.0.0.1", wantDashboard: "127.0.0.1:8888", wantPrometheus: "127.0.0.1:28686"},
		{name: "IPv6 loopback", dashboardHost: "::1", prometheusHost: "::1", wantDashboard: "[::1]:8888", wantPrometheus: "[::1]:28686"},
		{name: "hostname", dashboardHost: "localhost", prometheusHost: "localhost", wantDashboard: "localhost:8888", wantPrometheus: "localhost:28686"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := listenerTestConfig(test.dashboardHost, 8888, test.prometheusHost, 28686)
			dashboardAddress, prometheusAddress, err := listenerAddresses(config)
			if err != nil {
				t.Fatalf("listenerAddresses() error = %v", err)
			}
			if dashboardAddress != test.wantDashboard || prometheusAddress != test.wantPrometheus {
				t.Fatalf("addresses = %q, %q; want %q, %q", dashboardAddress, prometheusAddress, test.wantDashboard, test.wantPrometheus)
			}
		})
	}
}

func TestMalformedPrometheusHostIsRejectedBeforeDashboardPartialStartup(t *testing.T) {
	dashboardPort := kernelPort(t, "127.0.0.1")
	config := listenerTestConfig("127.0.0.1", dashboardPort, "http://127.0.0.1", kernelPort(t, "127.0.0.1"))
	statePath := filepath.Join(t.TempDir(), "state.json")

	err := runConfigured(context.Background(), config, statePath, 250*time.Millisecond, writeStateAtomic)
	if err == nil || !strings.Contains(err.Error(), "prometheus_listen_host") {
		t.Fatalf("runConfigured() error = %v, want malformed prometheus host", err)
	}
	assertCanRebind(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(dashboardPort)))
	if _, statErr := os.Stat(statePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid listener config partially started/checkpointed: %v", statErr)
	}
}

func TestDisabledListenersOpenNothing(t *testing.T) {
	ports := kernelPorts(t, "127.0.0.1", 2)
	dashboardPort, prometheusPort := ports[0], ports[1]
	config := listenerTestConfig("127.0.0.1", dashboardPort, "127.0.0.1", prometheusPort)
	config.EnableDash = false
	config.Prom = false

	services, err := prepareRuntimeServices(config)
	if err != nil {
		t.Fatalf("prepareRuntimeServices() error = %v", err)
	}
	if len(services) != 0 {
		t.Fatalf("disabled listener service count = %d, want 0", len(services))
	}
	assertCanRebind(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(dashboardPort)))
	assertCanRebind(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(prometheusPort)))
}

func TestConfiguredListenersCancelBoundedlyCloseWebSocketStopAndRebind(t *testing.T) {
	ports := kernelPorts(t, "127.0.0.1", 2)
	dashboardPort, prometheusPort := ports[0], ports[1]
	dashboardAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(dashboardPort))
	prometheusAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(prometheusPort))
	config := listenerTestConfig("127.0.0.1", dashboardPort, "127.0.0.1", prometheusPort)
	statePath := filepath.Join(t.TempDir(), "state.json")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- runConfigured(ctx, config, statePath, 500*time.Millisecond, writeStateAtomic)
	}()

	waitForHTTP(t, listenerURL(dashboardAddress, "/state"))
	waitForHTTP(t, listenerURL(prometheusAddress, "/metrics"))
	for _, route := range []string{"/", "/logs", "/logsenabled"} {
		waitForHTTP(t, listenerURL(dashboardAddress, route))
	}
	config.statsChan <- &promUpdate{metric: metricSigned, counter: 1, name: "listener-test", chainId: "listener-test-1", moniker: "listener-test"}
	waitForHTTPBody(t, listenerURL(prometheusAddress, "/metrics"), "tenderduty_signed_blocks")

	wsURL := (&url.URL{Scheme: "ws", Host: dashboardAddress, Path: "/ws"}).String()
	connection, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		cancel()
		<-runDone
		t.Fatalf("dial ordinarily open dashboard websocket: %v", err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, _, readErr := connection.ReadMessage()
		readDone <- readErr
	}()

	started := time.Now()
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("runConfigured() cancellation error = %v", err)
		}
	case <-time.After(listenerTestTimeout):
		t.Fatal("runtime cancellation exceeded tight listener shutdown bound")
	}
	if elapsed := time.Since(started); elapsed > listenerTestTimeout {
		t.Fatalf("runtime cancellation took %v", elapsed)
	}
	select {
	case readErr := <-readDone:
		if readErr == nil {
			t.Fatal("dashboard websocket remained ordinarily readable after shutdown")
		}
	case <-time.After(listenerTestTimeout):
		t.Fatal("dashboard shutdown did not unblock an open websocket")
	}
	_ = connection.Close()

	assertCannotConnect(t, dashboardAddress)
	assertCannotConnect(t, prometheusAddress)
	assertCanRebind(t, dashboardAddress)
	assertCanRebind(t, prometheusAddress)
	if _, _, err := loadState(statePath); err != nil {
		t.Fatalf("clean listener cancellation did not checkpoint state: %v", err)
	}
}

func TestIPv6LoopbackListenerUsesJoinedAddress(t *testing.T) {
	ports, err := reserveKernelPorts("::1", 2)
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	dashboardPort, prometheusPort := ports[0], ports[1]
	config := listenerTestConfig("::1", dashboardPort, "::1", prometheusPort)

	services, err := prepareRuntimeServices(config)
	if err != nil {
		t.Fatalf("prepareRuntimeServices() IPv6 error = %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("IPv6 service count = %d, want 2", len(services))
	}
	for _, service := range services {
		if !strings.HasPrefix(service.Addr().String(), "[::1]:") {
			t.Errorf("%s bound %q, want IPv6 loopback", service.Name(), service.Addr())
		}
		if err := service.Close(); err != nil {
			t.Errorf("close unstarted %s listener: %v", service.Name(), err)
		}
	}
}

func TestOccupiedSecondListenerRollsBackFirstListenerAndPropagatesStartupError(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	prometheusPort := occupied.Addr().(*net.TCPAddr).Port
	dashboardPort := kernelPort(t, "127.0.0.1")
	config := listenerTestConfig("127.0.0.1", dashboardPort, "127.0.0.1", prometheusPort)

	services, err := prepareRuntimeServices(config)
	if err == nil {
		for _, service := range services {
			_ = service.Close()
		}
		t.Fatal("occupied Prometheus listener did not propagate startup error")
	}
	if len(services) != 0 {
		t.Fatalf("failed startup returned %d partially owned services", len(services))
	}
	assertCanRebind(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(dashboardPort)))
}

func TestDashboardUpdateProducerUnblocksOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	config := &Config{updateChan: make(chan *dash.ChainStatus)}
	if config.emitDashboardUpdate(ctx, &dash.ChainStatus{Name: "canceled"}) {
		t.Fatal("canceled dashboard update was reported as delivered")
	}
}

func TestRunConfiguredRestoresProcessConfigAfterCancellation(t *testing.T) {
	previous := td
	baseline := &Config{}
	td = baseline
	t.Cleanup(func() { td = previous })
	config := listenerTestConfig("127.0.0.1", 0, "127.0.0.1", 0)
	config.EnableDash = false
	config.Prom = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runConfigured(ctx, config, filepath.Join(t.TempDir(), "state.json"), 100*time.Millisecond, writeStateAtomic); err != nil {
		t.Fatalf("already-canceled runConfigured() error = %v", err)
	}
	if td != baseline {
		t.Fatal("runConfigured left canceled runtime config published after returning")
	}
}

func TestShutdownKeepsAcceptedNotificationBeforeCheckpointWithListenerService(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	config := listenerTestConfig("127.0.0.1", kernelPort(t, "127.0.0.1"), "127.0.0.1", kernelPort(t, "127.0.0.1"))
	config.ctx = ctx
	config.cancel = cancel
	config.Prom = false

	services, err := prepareRuntimeServices(config)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	var eventMux sync.Mutex
	events := make([]string, 0, 3)
	record := func(event string) {
		eventMux.Lock()
		defer eventMux.Unlock()
		events = append(events, event)
	}
	lifecycle := newRuntimeLifecycle(config, statePath, 500*time.Millisecond, func(path string, state *savedState) error {
		record("checkpoint")
		return writeStateAtomic(path, state)
	})
	for _, service := range services {
		lifecycle.startService(service)
	}
	deliveryStarted := make(chan struct{})
	releaseDelivery := make(chan struct{})
	lifecycle.startNotificationWorker(func(*alertMsg) {
		close(deliveryStarted)
		<-releaseDelivery
		record("notification")
	})
	if !config.enqueueAlert(&alertMsg{message: "listener-order"}) {
		t.Fatal("notification was not accepted before listener shutdown")
	}
	<-deliveryStarted

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- lifecycle.shutdown() }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before accepted notification drain: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseDelivery)
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(listenerTestTimeout):
		t.Fatal("listener lifecycle did not join before checkpoint")
	}

	eventMux.Lock()
	got := fmt.Sprint(events)
	eventMux.Unlock()
	if got != "[notification checkpoint]" {
		t.Fatalf("shutdown event order = %s, want accepted notification before checkpoint", got)
	}
}
