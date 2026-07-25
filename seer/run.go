package seer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/signal"
	"strings"
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
	services      []runtimeService
	serviceWG     sync.WaitGroup
	serviceErrMux sync.Mutex
	serviceErr    error
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

func (r *runtimeLifecycle) startService(service runtimeService) {
	r.services = append(r.services, service)
	done := service.Start()
	log.Printf("starting %s listener on %s", service.Name(), service.Addr())
	r.serviceWG.Add(1)
	go func() {
		defer r.serviceWG.Done()
		if err := <-done; err != nil {
			r.serviceErrMux.Lock()
			if r.serviceErr == nil {
				r.serviceErr = fmt.Errorf("%s listener failed: %w", service.Name(), err)
			}
			r.serviceErrMux.Unlock()
			r.config.cancel()
		}
	}()
}

func (r *runtimeLifecycle) runtimeServiceError() error {
	r.serviceErrMux.Lock()
	defer r.serviceErrMux.Unlock()
	return r.serviceErr
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

func (r *runtimeLifecycle) shutdownServices(ctx context.Context) error {
	var workers sync.WaitGroup
	errorsFound := make(chan error, len(r.services))
	for _, service := range r.services {
		service := service
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := service.Shutdown(ctx); err != nil {
				errorsFound <- fmt.Errorf("stop %s listener: %w", service.Name(), err)
			}
		}()
	}
	if err := waitForGroups(ctx, &workers); err != nil {
		return err
	}
	close(errorsFound)
	var joined error
	for err := range errorsFound {
		joined = errors.Join(joined, err)
	}
	return joined
}

func (r *runtimeLifecycle) shutdown() error {
	r.shutdownOnce.Do(func() {
		r.config.stopAlertIngress()
		r.config.cancel()
		r.config.closeWebSockets()

		drainContext, cancel := context.WithTimeout(context.Background(), r.drainTimeout)
		defer cancel()
		serviceShutdownErr := r.shutdownServices(drainContext)
		if err := waitForGroups(drainContext, &r.monitors, &r.config.ingressWG, &r.serviceWG); err != nil {
			r.shutdownErr = fmt.Errorf("%w while quiescing monitoring, listeners, and alert ingress: %v", errShutdownDrainTimeout, err)
			return
		}

		close(r.config.alertChan)
		if err := waitForGroups(drainContext, &r.notifications); err != nil {
			r.shutdownErr = fmt.Errorf("%w while draining accepted notifications: %v", errShutdownDrainTimeout, err)
			return
		}
		if serviceShutdownErr != nil {
			if errors.Is(serviceShutdownErr, context.DeadlineExceeded) || errors.Is(serviceShutdownErr, context.Canceled) {
				r.shutdownErr = fmt.Errorf("%w while stopping runtime listeners: %v", errShutdownDrainTimeout, serviceShutdownErr)
			} else {
				r.shutdownErr = serviceShutdownErr
			}
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

func refreshRegistryMonitor(ctx context.Context) {
	if err := refreshRegistry(); err != nil {
		l("could not fetch chain registry paths, using defaults")
	}
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l("refreshing cosmos.registry paths")
			if err := refreshRegistry(); err != nil {
				l("could not refresh registry paths -", err)
			}
		}
	}
}

func drainChannel[T any](ctx context.Context, channel <-chan T) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-channel:
			if !ok {
				return
			}
		}
	}
}

func runConfigured(
	parent context.Context,
	config *Config,
	stateFile string,
	drainTimeout time.Duration,
	checkpoint func(string, *savedState) error,
) error {
	if parent == nil {
		parent = context.Background()
	}
	if config.cancel != nil {
		config.cancel()
	}
	config.ctx, config.cancel = context.WithCancel(parent)
	if config.Chains == nil {
		config.Chains = make(map[string]*ChainConfig)
	}
	if config.alertChan == nil {
		config.alertChan = make(chan *alertMsg, notificationQueueCapacity)
	}
	if config.updateChan == nil {
		config.updateChan = make(chan *dash.ChainStatus, len(config.Chains)*2+1)
	}
	if config.logChan == nil {
		config.logChan = make(chan dash.LogMessage)
	}
	if config.statsChan == nil {
		config.statsChan = make(chan *promUpdate, len(config.Chains)*2+1)
	}
	if config.alarms == nil {
		config.alarms = newAlarmCache()
	}
	config.bindDurableState()
	previousConfig := td
	td = config
	defer func() { td = previousConfig }()
	if parent.Err() != nil {
		config.cancel()
		return nil
	}

	fatal, problems := validateConfig(config)
	for _, problem := range problems {
		fmt.Println(problem)
	}
	if fatal {
		config.cancel()
		return fmt.Errorf("NosNode Seer configuration is invalid; refusing to start: %s", strings.Join(problems, "; "))
	}

	services, err := prepareRuntimeServices(config)
	if err != nil {
		config.cancel()
		return err
	}
	log.Println("NosNode Seer config is valid; beginning the watch with", len(config.Chains), "chains")
	notifications.cache = config.alarms
	lifecycle := newRuntimeLifecycle(config, stateFile, drainTimeout, checkpoint)
	for _, service := range services {
		lifecycle.startService(service)
	}
	lifecycle.startNotificationWorker(deliverAlert)

	if !config.EnableDash {
		lifecycle.startMonitor(func(ctx context.Context) { drainChannel(ctx, config.updateChan) })
	}
	if !config.Prom {
		lifecycle.startMonitor(func(ctx context.Context) { drainChannel(ctx, config.statsChan) })
	}
	if config.publicFallback {
		lifecycle.startMonitor(refreshRegistryMonitor)
	}
	if config.Healthcheck.Enabled {
		lifecycle.startMonitor(func(ctx context.Context) { config.pingHealthcheck(ctx) })
	}
	for name, chain := range config.Chains {
		chain := chain
		name := name
		lifecycle.startMonitor(func(ctx context.Context) { chain.watch(ctx) })
		lifecycle.startMonitor(func(ctx context.Context) { chain.monitorHealth(ctx, name) })
		lifecycle.startMonitor(func(ctx context.Context) { monitorChain(ctx, chain) })
	}

	<-config.ctx.Done()
	shutdownErr := lifecycle.shutdown()
	return errors.Join(lifecycle.runtimeServiceError(), shutdownErr)
}

func Run(configFile, stateFile, chainConfigDirectory string, password *string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	log.Println("durable state checkpoint handler ready")

	config, err := loadConfig(configFile, stateFile, chainConfigDirectory, password)
	if err != nil {
		return err
	}
	return runConfigured(ctx, config, stateFile, shutdownDrainTimeout, writeStateAtomic)
}
