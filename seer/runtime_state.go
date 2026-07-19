package seer

import (
	"context"
	"sync"
	"time"
)

// nodeRuntimeState is copied while holding the chain's shared durable-state lock.
type nodeRuntimeState struct {
	down      bool
	wasDown   bool
	syncing   bool
	lastMsg   string
	downSince time.Time
}

func (c *Config) bindDurableState() {
	if c.alarms == nil {
		c.alarms = newAlarmCache()
	}
	c.alarms.stateMux = &c.stateMux
	for _, chain := range c.Chains {
		chain.stateMux = &c.stateMux
	}
}

func (c *Config) alarmState() *alarmCache {
	if c.alarms != nil {
		return c.alarms
	}
	return alarms
}

func (c *Config) beginAlertIngress() bool {
	c.ingressMux.Lock()
	defer c.ingressMux.Unlock()
	if !c.accepting {
		return false
	}
	c.ingressWG.Add(1)
	return true
}

func (c *Config) finishAlertIngress() {
	c.ingressWG.Done()
}

func (c *Config) enqueueAlert(message *alertMsg) bool {
	if !c.beginAlertIngress() {
		return false
	}
	defer c.finishAlertIngress()
	c.alertChan <- message
	return true
}

func (c *Config) startAlertIngress() {
	c.ingressMux.Lock()
	c.accepting = true
	c.ingressMux.Unlock()
}

func (c *Config) stopAlertIngress() {
	c.ingressMux.Lock()
	c.accepting = false
	c.ingressMux.Unlock()
}

func (cc *ChainConfig) durableStateMux() *sync.RWMutex {
	if cc.stateMux != nil {
		return cc.stateMux
	}
	return &cc.localStateMux
}

func (c *Config) emitStat(ctx context.Context, update *promUpdate) bool {
	select {
	case c.statsChan <- update:
		return true
	case <-ctx.Done():
		return false
	}
}

func (cc *ChainConfig) setNoNodes(noNodes bool) {
	mux := cc.durableStateMux()
	mux.Lock()
	cc.noNodes = noNodes
	mux.Unlock()
}

func (cc *ChainConfig) noNodesState() bool {
	mux := cc.durableStateMux()
	mux.RLock()
	defer mux.RUnlock()
	return cc.noNodes
}

func (cc *ChainConfig) recordBlockResult(result int) []int {
	mux := cc.durableStateMux()
	mux.Lock()
	defer mux.Unlock()
	if len(cc.blocksResults) == 0 {
		cc.blocksResults = []int{result}
	} else {
		cc.blocksResults = append([]int{result}, cc.blocksResults[:len(cc.blocksResults)-1]...)
	}
	return append([]int(nil), cc.blocksResults...)
}

func (cc *ChainConfig) blockResultsSnapshot() []int {
	mux := cc.durableStateMux()
	mux.RLock()
	defer mux.RUnlock()
	return append([]int(nil), cc.blocksResults...)
}

func (cc *ChainConfig) updateNodeState(node *NodeConfig, update func(*nodeRuntimeState)) nodeRuntimeState {
	mux := cc.durableStateMux()
	mux.Lock()
	defer mux.Unlock()
	state := nodeRuntimeState{
		down:      node.down,
		wasDown:   node.wasDown,
		syncing:   node.syncing,
		lastMsg:   node.lastMsg,
		downSince: node.downSince,
	}
	update(&state)
	node.down = state.down
	node.wasDown = state.wasDown
	node.syncing = state.syncing
	node.lastMsg = state.lastMsg
	node.downSince = state.downSince
	return state
}

func (cc *ChainConfig) nodeState(node *NodeConfig) nodeRuntimeState {
	mux := cc.durableStateMux()
	mux.RLock()
	defer mux.RUnlock()
	return nodeRuntimeState{
		down:      node.down,
		wasDown:   node.wasDown,
		syncing:   node.syncing,
		lastMsg:   node.lastMsg,
		downSince: node.downSince,
	}
}
