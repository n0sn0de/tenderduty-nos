package seer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	dash "github.com/n0sn0de/tenderduty-nos/seer/dashboard"
)

const shutdownDrainTimeout = 20 * time.Second

var (
	td                      = &Config{}
	errShutdownDrainTimeout = errors.New("shutdown drain timed out")
)

func notificationWorker(alerts <-chan *alertMsg, deliver func(*alertMsg)) {
	for alert := range alerts {
		deliver(alert)
	}
}

func deliverAlert(msg *alertMsg) {
	deliveries := []func() error{
		func() error { return notifyPagerduty(msg) },
		func() error { return notifyDiscord(msg) },
		func() error { return notifyTg(msg) },
		func() error { return notifySlack(msg) },
	}
	var workers sync.WaitGroup
	workers.Add(len(deliveries))
	for _, deliver := range deliveries {
		go func() {
			defer workers.Done()
			_ = deliver()
		}()
	}
	workers.Wait()
}

type runtimeLifecycle struct {
	config        *Config
	stateFile     string
	drainTimeout  time.Duration
	checkpoint    func(string, *savedState) error
	monitors      sync.WaitGroup
	notifications sync.WaitGroup
	shutdownOnce  sync.Once
	shutdownErr   error
}

func newRuntimeLifecycle(
	config *Config,
	stateFile string,
	drainTimeout time.Duration,
	checkpoint func(string, *savedState) error,
) *runtimeLifecycle {
	if config.ctx == nil || config.cancel == nil {
		config.ctx, config.cancel = context.WithCancel(context.Background())
	}
	if config.alertChan == nil {
		config.alertChan = make(chan *alertMsg, notificationQueueCapacity)
	}
	config.bindDurableState()
	config.startAlertIngress()
	return &runtimeLifecycle{
		config:       config,
		stateFile:    stateFile,
		drainTimeout: drainTimeout,
		checkpoint:   checkpoint,
	}
}

func (r *runtimeLifecycle) startMonitor(run func(context.Context)) {
	r.monitors.Add(1)
	go func() {
		defer r.monitors.Done()
		run(r.config.ctx)
	}()
}

func (r *runtimeLifecycle) startNotificationWorker(deliver func(*alertMsg)) {
	r.notifications.Add(1)
	go func() {
		defer r.notifications.Done()
		notificationWorker(r.config.alertChan, deliver)
	}()
}

func waitForGroups(ctx context.Context, groups ...*sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		for _, group := range groups {
			group.Wait()
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *runtimeLifecycle) shutdown() error {
	r.shutdownOnce.Do(func() {
		r.config.stopAlertIngress()
		r.config.cancel()
		r.config.closeWebSockets()

		drainContext, cancel := context.WithTimeout(context.Background(), r.drainTimeout)
		defer cancel()
		if err := waitForGroups(drainContext, &r.monitors, &r.config.ingressWG); err != nil {
			r.shutdownErr = fmt.Errorf("%w while quiescing monitoring and alert ingress: %v", errShutdownDrainTimeout, err)
			return
		}

		close(r.config.alertChan)
		if err := waitForGroups(drainContext, &r.notifications); err != nil {
			r.shutdownErr = fmt.Errorf("%w while draining accepted notifications: %v", errShutdownDrainTimeout, err)
			return
		}

		log.Println("saving durable state...")
		if err := r.checkpoint(r.stateFile, snapshotSavedState(r.config)); err != nil {
			r.shutdownErr = fmt.Errorf("save durable state: %w", err)
			return
		}
		log.Printf("saved durable state version %d to %s", currentStateVersion, r.stateFile)
		log.Println("NosNode Seer exiting.")
	})
	return r.shutdownErr
}

func monitorChain(ctx context.Context, chain *ChainConfig) {
	for ctx.Err() == nil {
		if err := chain.newRpc(ctx); err != nil {
			l(chain.ChainId, err)
			if !waitForContext(ctx, 5*time.Second) {
				return
			}
			continue
		}
		if err := chain.GetValInfo(ctx, true); err != nil {
			l("🛑", chain.ChainId, err)
		}
		chain.WsRun(ctx)
		if ctx.Err() != nil {
			return
		}
		l(chain.ChainId, "🌀 websocket exited! Restarting monitoring")
		if !waitForContext(ctx, 5*time.Second) {
			return
		}
	}
}

func waitForContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func Run(configFile, stateFile, chainConfigDirectory string, password *string) error {
	var err error
	td, err = loadConfig(configFile, stateFile, chainConfigDirectory, password)
	if err != nil {
		return err
	}

	// Register termination before validation can launch even non-durable helper workers.
	quitting := make(chan os.Signal, 1)
	signal.Notify(quitting, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(quitting)
	log.Println("durable state checkpoint handler ready")

	fatal, problems := validateConfig(td)
	for _, problem := range problems {
		fmt.Println(problem)
	}
	if fatal {
		log.Fatal("NosNode Seer configuration is invalid; refusing to start")
	}
	log.Println("NosNode Seer config is valid; beginning the watch with", len(td.Chains), "chains")

	notifications.cache = td.alarms
	lifecycle := newRuntimeLifecycle(td, stateFile, shutdownDrainTimeout, writeStateAtomic)
	select {
	case <-quitting:
		return lifecycle.shutdown()
	default:
	}
	lifecycle.startNotificationWorker(deliverAlert)

	if td.EnableDash {
		go dash.Serve(td.Listen, td.updateChan, td.logChan, td.HideLogs)
		l("starting dashboard on", td.Listen)
	} else {
		go func() {
			for range td.updateChan {
			}
		}()
	}
	if td.Prom {
		go prometheusExporter(td.ctx, td.statsChan)
	} else {
		go func() {
			for range td.statsChan {
			}
		}()
	}

	if td.Healthcheck.Enabled {
		lifecycle.startMonitor(func(ctx context.Context) { td.pingHealthcheck(ctx) })
	}
	for name, chain := range td.Chains {
		chain := chain
		name := name
		lifecycle.startMonitor(func(ctx context.Context) { chain.watch(ctx) })
		lifecycle.startMonitor(func(ctx context.Context) { chain.monitorHealth(ctx, name) })
		lifecycle.startMonitor(func(ctx context.Context) { monitorChain(ctx, chain) })
	}

	select {
	case <-quitting:
	case <-td.ctx.Done():
	}
	return lifecycle.shutdown()
}
