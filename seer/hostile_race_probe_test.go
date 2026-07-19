package seer

import (
	"encoding/json"
	"testing"
	"time"
)

func TestConcurrentPersistedMutationsAndSnapshotAreRaceFree(t *testing.T) {
	cache := newAlarmCache()
	node := &NodeConfig{Url: "http://127.0.0.1:26657"}
	chain := &ChainConfig{blocksResults: []int{0, 0, 0}, Nodes: []*NodeConfig{node}}
	config := &Config{
		EnableDash: true,
		alarms:     cache,
		Chains:     map[string]*ChainConfig{"probe": chain},
	}
	config.bindDurableState()

	trigger := &alertMsg{message: "accepted-delivery"}
	resolution := &alertMsg{message: trigger.message, resolved: true}
	start := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-start
		for generation := 1; generation <= 2_000; generation++ {
			chain.recordBlockResult(generation)
			chain.updateNodeState(node, func(state *nodeRuntimeState) {
				state.down = generation%2 == 0
				state.downSince = time.Unix(int64(generation), 0)
			})
			if cache.reserveDelivery(trigger, slk) {
				cache.completeDelivery(trigger, slk, true)
			}
			if cache.reserveDelivery(resolution, slk) {
				cache.completeDelivery(resolution, slk, true)
			}
		}
	}()

	close(start)
	for range 2_000 {
		if _, err := json.Marshal(snapshotSavedState(config)); err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
	}
	<-done
}

func TestSnapshotSavedStateIsOneCoherentInstant(t *testing.T) {
	cache := newAlarmCache()
	node := &NodeConfig{Url: "http://127.0.0.1:26657"}
	chain := &ChainConfig{blocksResults: []int{0}, Nodes: []*NodeConfig{node}}
	config := &Config{
		EnableDash: true,
		alarms:     cache,
		Chains:     map[string]*ChainConfig{"coherent": chain},
	}
	config.bindDurableState()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for generation := 1; generation <= 5_000; generation++ {
			when := time.Unix(int64(generation), 0).UTC()
			config.stateMux.Lock()
			chain.blocksResults[0] = generation
			node.down = true
			node.downSince = when
			cache.notifyMux.Lock()
			cache.SentSlkAlarms["generation"] = when
			cache.notifyMux.Unlock()
			config.stateMux.Unlock()
		}
	}()

	for {
		snapshot := snapshotSavedState(config)
		blockGeneration := snapshot.Blocks["coherent"][0]
		nodeGeneration := snapshot.NodesDown["coherent"][node.Url].Unix()
		alarmGeneration := snapshot.Alarms.SentSlkAlarms["generation"].Unix()
		if blockGeneration != 0 && (int64(blockGeneration) != nodeGeneration || nodeGeneration != alarmGeneration) {
			t.Fatalf("cross-moment snapshot: block=%d node=%d alarm=%d", blockGeneration, nodeGeneration, alarmGeneration)
		}
		select {
		case <-done:
			return
		default:
		}
	}
}
