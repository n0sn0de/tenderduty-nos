package seer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	dash "github.com/n0sn0de/tenderduty-nos/seer/dashboard"
	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
)

// newRpc sets up the rpc client used for monitoring. It will try nodes in order until a working node is found.
// It never holds the durable-state lock while performing network I/O.
func (cc *ChainConfig) newRpc(parent context.Context) error {
	cc.rpcMux.Lock()
	defer cc.rpcMux.Unlock()
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	var anyWorking bool
	for _, endpoint := range cc.Nodes {
		anyWorking = anyWorking || !cc.nodeState(endpoint).down
	}
	tryURL := func(endpointURL string) (client *rpchttp.HTTP, msg string, down, syncing bool) {
		if _, err := url.Parse(endpointURL); err != nil {
			msg = fmt.Sprintf("❌ could not parse url %s: (%s) %s", cc.name, endpointURL, err)
			l(msg)
			return nil, msg, true, false
		}
		candidate, err := rpchttp.New(endpointURL, "/websocket")
		if err != nil {
			msg = fmt.Sprintf("❌ could not connect client for %s: (%s) %s", cc.name, endpointURL, err)
			l(msg)
			return nil, msg, true, false
		}
		status, err := candidate.Status(ctx)
		if err != nil {
			msg = fmt.Sprintf("❌ could not get status for %s: (%s) %s", cc.name, endpointURL, err)
			l(msg)
			return nil, msg, true, false
		}
		if status.NodeInfo.Network != cc.ChainId {
			msg = fmt.Sprintf("chain id %s on %s does not match, expected %s, skipping", status.NodeInfo.Network, endpointURL, cc.ChainId)
			l(msg)
			return nil, msg, true, false
		}
		if status.SyncInfo.CatchingUp {
			msg = fmt.Sprint("🐢 node is not synced, skipping ", endpointURL)
			l(msg)
			return nil, msg, true, true
		}
		return candidate, "", false, false
	}
	markDown := func(endpoint *NodeConfig, message string, syncing bool) {
		cc.updateNodeState(endpoint, func(state *nodeRuntimeState) {
			if !state.down {
				state.down = true
				state.downSince = time.Now()
			}
			state.syncing = syncing
			state.lastMsg = message
		})
	}

	for _, endpoint := range cc.Nodes {
		if anyWorking && cc.nodeState(endpoint).down {
			continue
		}
		client, message, failed, syncing := tryURL(endpoint.Url)
		if failed {
			markDown(endpoint, message, syncing)
			continue
		}
		cc.client = client
		cc.setNoNodes(false)
		return nil
	}
	if cc.PublicFallback {
		if registryURL, ok := getRegistryUrl(cc.ChainId); ok {
			node := guessPublicEndpointContext(ctx, registryURL)
			l(cc.ChainId, "⛑ attemtping to use public fallback node", node)
			if client, _, failed, _ := tryURL(node); !failed {
				cc.client = client
				cc.setNoNodes(false)
				l(cc.ChainId, "⛑ connected to public endpoint", node)
				return nil
			}
		} else {
			l("could not find a public endpoint for", cc.ChainId)
		}
	}
	cc.setNoNodes(true)
	cc.activeAlerts = td.alarmState().getCount(cc.name)
	cc.lastError = "no usable RPC endpoints available for " + cc.ChainId
	if td.EnableDash {
		td.updateChan <- &dash.ChainStatus{
			MsgType:      "status",
			Name:         cc.name,
			ChainId:      cc.ChainId,
			Moniker:      cc.valInfo.Moniker,
			Bonded:       cc.valInfo.Bonded,
			Jailed:       cc.valInfo.Jailed,
			Tombstoned:   cc.valInfo.Tombstoned,
			Missed:       cc.valInfo.Missed,
			Window:       cc.valInfo.Window,
			Nodes:        len(cc.Nodes),
			HealthyNodes: 0,
			ActiveAlerts: cc.activeAlerts,
			Height:       0,
			LastError:    cc.lastError,
			Blocks:       cc.blockResultsSnapshot(),
		}
	}
	return errors.New("no usable endpoints available for " + cc.ChainId)
}

func (cc *ChainConfig) monitorHealth(ctx context.Context, chainName string) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			var probes sync.WaitGroup
			for _, node := range cc.Nodes {
				node := node
				probes.Add(1)
				go func() {
					defer probes.Done()
					markUnhealthy := func(message string) {
						state := cc.updateNodeState(node, func(state *nodeRuntimeState) {
							state.lastMsg = fmt.Sprintf("%-12s node %s is %s", chainName, node.Url, message)
							if !state.down {
								state.down = true
								if node.AlertIfDown {
									state.downSince = time.Now()
								}
							}
						})
						if td.Prom && node.AlertIfDown {
							td.emitStat(ctx, cc.mkUpdate(metricNodeDownSeconds, time.Since(state.downSince).Seconds(), node.Url))
						}
						l("⚠️ " + state.lastMsg)
					}
					client, err := rpchttp.New(node.Url, "/websocket")
					if err != nil {
						markUnhealthy(err.Error())
						return
					}
					requestContext, cancel := context.WithTimeout(ctx, 10*time.Second)
					status, err := client.Status(requestContext)
					cancel()
					if err != nil {
						markUnhealthy("down")
						return
					}
					if status.NodeInfo.Network != cc.ChainId {
						markUnhealthy("on the wrong network")
						return
					}
					if status.SyncInfo.CatchingUp {
						markUnhealthy("not synced")
						cc.updateNodeState(node, func(state *nodeRuntimeState) { state.syncing = true })
						return
					}

					wasDown := false
					cc.updateNodeState(node, func(state *nodeRuntimeState) {
						wasDown = state.down
						if state.down {
							state.lastMsg = ""
							state.wasDown = true
						}
						state.down = false
						state.syncing = false
						state.downSince = time.Unix(0, 0)
					})
					cc.setNoNodes(false)
					if td.Prom {
						td.emitStat(ctx, cc.mkUpdate(metricNodeDownSeconds, 0, node.Url))
					}
					if wasDown {
						l(fmt.Sprintf("🟢 %-12s node %s is healthy", chainName, node.Url))
					}
				}()
			}
			probes.Wait()
			if ctx.Err() != nil {
				return
			}
			if cc.client == nil {
				if err := cc.newRpc(ctx); err != nil {
					l("💥", cc.ChainId, err)
				}
			}
			if cc.valInfo != nil {
				cc.lastValInfo = &ValInfo{
					Moniker:    cc.valInfo.Moniker,
					Bonded:     cc.valInfo.Bonded,
					Jailed:     cc.valInfo.Jailed,
					Tombstoned: cc.valInfo.Tombstoned,
					Missed:     cc.valInfo.Missed,
					Window:     cc.valInfo.Window,
					Conspub:    cc.valInfo.Conspub,
					Valcons:    cc.valInfo.Valcons,
				}
			}
			if err := cc.GetValInfo(ctx, false); err != nil {
				l("❓ refreshing signing info for", cc.ValAddress, err)
			}
		}
	}
}

func (c *Config) pingHealthcheck(ctx context.Context) {
	if !c.Healthcheck.Enabled {
		return
	}
	ticker := time.NewTicker(c.Healthcheck.PingRate * time.Second)
	defer ticker.Stop()
	client := &http.Client{Timeout: 10 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Healthcheck.PingURL, nil)
			var response *http.Response
			if err == nil {
				response, err = client.Do(request)
			}
			if err != nil {
				l(fmt.Sprintf("❌ Failed to ping healthcheck URL: %s", err.Error()))
			} else {
				_ = response.Body.Close()
				l(fmt.Sprintf("🏓 Successfully pinged healthcheck URL: %s", c.Healthcheck.PingURL))
			}
		}
	}
}

// endpointRex matches the first a tag's hostname and port if present.
var endpointRex = regexp.MustCompile(`//([^/:]+)(:\d+)?`)

// guessPublicEndpointContext attempts to deal with a shortcoming in the Tendermint RPC client that does not allow path prefixes.
func guessPublicEndpointContext(parent context.Context, u string) string {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"/", nil)
	if err != nil {
		return u
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		return u
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return u
	}
	matches := endpointRex.FindStringSubmatch(string(body))
	if len(matches) < 2 {
		return u
	}
	proto := "https://"
	port := ":443"
	if len(matches) == 3 && matches[2] != "" && matches[2] != ":443" {
		proto = "http://"
		port = matches[2]
	}
	return proto + matches[1] + port
}
