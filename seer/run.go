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

var td = &Config{}

func notificationWorker(ctx context.Context, alerts <-chan *alertMsg, deliver func(*alertMsg)) {
	for {
		select {
		case alert, ok := <-alerts:
			if !ok {
				return
			}
			deliver(alert)
		case <-ctx.Done():
			return
		}
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

func Run(configFile, stateFile, chainConfigDirectory string, password *string) error {
	var err error
	td, err = loadConfig(configFile, stateFile, chainConfigDirectory, password)
	if err != nil {
		return err
	}
	fatal, problems := validateConfig(td)
	for _, p := range problems {
		fmt.Println(p)
	}
	if fatal {
		log.Fatal("NosNode Seer configuration is invalid; refusing to start")
	}
	log.Println("NosNode Seer config is valid; beginning the watch with", len(td.Chains), "chains")

	defer td.cancel()

	go notificationWorker(td.ctx, td.alertChan, deliverAlert)

	if td.EnableDash {
		go dash.Serve(td.Listen, td.updateChan, td.logChan, td.HideLogs)
		l("starting dashboard on", td.Listen)
	} else {
		go func() {
			for {
				<-td.updateChan
			}
		}()
	}
	if td.Prom {
		go prometheusExporter(td.ctx, td.statsChan)
	} else {
		go func() {
			for {
				<-td.statsChan
			}
		}()
	}

	// NosNode Seer health checks:
	if td.Healthcheck.Enabled {
		td.pingHealthcheck()
	}

	for k := range td.Chains {
		cc := td.Chains[k]

		go func(cc *ChainConfig, name string) {
			// alert worker
			go cc.watch()

			// node health checks:
			go func() {
				for {
					cc.monitorHealth(td.ctx, name)
				}
			}()

			// websocket subscription and occasional validator info refreshes
			for {
				e := cc.newRpc()
				if e != nil {
					l(cc.ChainId, e)
					time.Sleep(5 * time.Second)
					continue
				}
				e = cc.GetValInfo(true)
				if e != nil {
					l("🛑", cc.ChainId, e)
				}
				cc.WsRun()
				l(cc.ChainId, "🌀 websocket exited! Restarting monitoring")
				time.Sleep(5 * time.Second)
			}
		}(cc, k)
	}

	saved := make(chan error, 1)
	go saveOnExit(td, stateFile, saved)

	<-td.ctx.Done()
	return errors.Join(err, <-saved)
}

func saveOnExit(config *Config, stateFile string, saved chan<- error) {
	quitting := make(chan os.Signal, 1)
	signal.Notify(quitting, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(quitting)
	log.Println("durable state checkpoint handler ready")

	saveState := func() error {
		log.Println("saving durable state...")
		if err := writeStateAtomic(stateFile, snapshotSavedState(config)); err != nil {
			return fmt.Errorf("save durable state: %w", err)
		}
		log.Printf("saved durable state version %d to %s", currentStateVersion, stateFile)
		log.Println("NosNode Seer exiting.")
		return nil
	}

	select {
	case <-config.ctx.Done():
		saved <- saveState()
	case <-quitting:
		err := saveState()
		config.cancel()
		saved <- err
	}
	close(saved)
}
