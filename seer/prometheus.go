package seer

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	promMux                      sync.RWMutex
	notificationDeliveryOutcomes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tenderduty_notification_delivery_attempts_total",
		Help: "Notification delivery attempts by bounded destination and outcome labels.",
	}, []string{"destination", "outcome"})
	prometheusMetricsOnce sync.Once
	prometheusMetrics     metrics
)

func observeNotificationDelivery(destination, outcome string) {
	notificationDeliveryOutcomes.WithLabelValues(destination, outcome).Inc()
	log.Printf("notification delivery destination=%s outcome=%s", destination, outcome)
}

type metricType uint8

const (
	metricSigned metricType = iota
	metricProposed
	metricMissed
	metricPrevote
	metricPrecommit
	metricConsecutive
	metricWindowMissed
	metricWindowSize
	metricLastBlockSeconds
	metricLastBlockSecondsNotFinal

	metricTotalNodes
	metricUnealthyNodes
	metricNodeLagSeconds
	metricNodeDownSeconds
)

type promUpdate struct {
	metric   metricType
	counter  float64
	name     string
	chainId  string
	moniker  string
	endpoint string
}

type metrics map[metricType]*prometheus.GaugeVec

func (m metrics) setStat(update *promUpdate) {
	labels := map[string]string{
		"name":     update.name,
		"chain_id": update.chainId,
		"moniker":  update.moniker,
	}
	promMux.RLock()
	defer promMux.RUnlock()
	if update.metric == metricNodeLagSeconds || update.metric == metricNodeDownSeconds {
		labels["endpoint"] = update.endpoint
	}
	m[update.metric].With(labels).Set(update.counter)
}

func currentPrometheusMetrics() metrics {
	prometheusMetricsOnce.Do(func() {
		chainLabels := []string{"name", "chain_id", "moniker"}
		hostLabels := []string{"name", "chain_id", "moniker", "endpoint"}
		prometheusMetrics = metrics{
			metricSigned: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_signed_blocks",
				Help: "count of blocks signed since NosNode Seer was started",
			}, chainLabels),
			metricProposed: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_proposed_blocks",
				Help: "count of blocks proposed since NosNode Seer was started",
			}, chainLabels),
			metricMissed: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_missed_blocks",
				Help: "count of blocks missed without seeing a precommit or prevote since NosNode Seer was started",
			}, chainLabels),
			metricPrevote: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_missed_blocks_prevote_present",
				Help: "count of blocks missed where a prevote was seen since NosNode Seer was started",
			}, chainLabels),
			metricPrecommit: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_missed_blocks_precommit_present",
				Help: "count of blocks missed where a precommit was seen since NosNode Seer was started",
			}, chainLabels),
			metricConsecutive: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_consecutive_missed_blocks",
				Help: "the current count of consecutively missed blocks regardless of precommit or prevote status",
			}, chainLabels),
			metricWindowSize: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_missed_block_window",
				Help: "the missed block aka slashing window",
			}, chainLabels),
			metricWindowMissed: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_missed_blocks_for_window",
				Help: "the current count of missed blocks in the slashing window regardless of precommit or prevote status",
			}, chainLabels),
			metricLastBlockSeconds: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_time_since_last_block",
				Help: "how many seconds since the previous block was finalized, only set when a new block is seen, not useful for stall detection, helpful for averaging times",
			}, chainLabels),
			metricLastBlockSecondsNotFinal: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_time_since_last_block_unfinalized",
				Help: "how many seconds since the previous block was finalized, set regardless of finalization, useful for stall detection, not helpful for figuring average time",
			}, chainLabels),
			metricTotalNodes: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_total_monitored_endpoints",
				Help: "the count of rpc endpoints being monitored for a chain",
			}, chainLabels),
			metricUnealthyNodes: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_total_unhealthy_endpoints",
				Help: "the count of unhealthy rpc endpoints being monitored for a chain",
			}, chainLabels),
			metricNodeLagSeconds: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_endpoint_syncing_seconds_behind",
				Help: "how many seconds a node is behind the head of a chain",
			}, hostLabels),
			metricNodeDownSeconds: promauto.NewGaugeVec(prometheus.GaugeOpts{
				Name: "tenderduty_endpoint_down_seconds",
				Help: "how many seconds a node has been marked as unhealthy",
			}, hostLabels),
		}
	})
	return prometheusMetrics
}

type prometheusServer struct {
	ctx        context.Context
	cancel     context.CancelFunc
	updates    <-chan *promUpdate
	metrics    metrics
	listener   net.Listener
	httpServer *http.Server
	workers    sync.WaitGroup
	startOnce  sync.Once
	done       chan error

	shutdownOnce sync.Once
	shutdownErr  error
}

func newPrometheusServer(listener net.Listener, updates <-chan *promUpdate) *prometheusServer {
	ctx, cancel := context.WithCancel(context.Background())
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	return &prometheusServer{
		ctx:      ctx,
		cancel:   cancel,
		updates:  updates,
		metrics:  currentPrometheusMetrics(),
		listener: listener,
		httpServer: &http.Server{
			Handler:           mux,
			ReadTimeout:       20 * time.Second,
			WriteTimeout:      20 * time.Second,
			IdleTimeout:       120 * time.Second,
			ReadHeaderTimeout: 20 * time.Second,
			BaseContext: func(net.Listener) context.Context {
				return ctx
			},
		},
		done: make(chan error, 1),
	}
}

func (s *prometheusServer) Name() string   { return "prometheus" }
func (s *prometheusServer) Addr() net.Addr { return s.listener.Addr() }

func (s *prometheusServer) Start() <-chan error {
	s.startOnce.Do(func() {
		s.workers.Add(1)
		go s.updateMetrics()
		go func() {
			err := s.httpServer.Serve(s.listener)
			if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
				err = nil
			}
			s.done <- err
			close(s.done)
		}()
	})
	return s.done
}

func (s *prometheusServer) updateMetrics() {
	defer s.workers.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case update, ok := <-s.updates:
			if !ok {
				return
			}
			if update != nil {
				s.metrics.setStat(update)
			}
		}
	}
}

func (s *prometheusServer) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.cancel()
		httpErr := s.httpServer.Shutdown(ctx)
		_ = s.listener.Close()
		workersErr := waitForGroups(ctx, &s.workers)
		s.shutdownErr = errors.Join(httpErr, workersErr)
	})
	return s.shutdownErr
}

func (s *prometheusServer) Close() error {
	s.cancel()
	return errors.Join(s.listener.Close(), s.httpServer.Close())
}
