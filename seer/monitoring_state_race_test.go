package seer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConcurrentRPCValidatorRefreshAndWebSocketWorkloadIsRaceFree(t *testing.T) {
	const chainID = "fixture-1"
	const validatorAddress = fixtureValoper

	websocketStarted := make(chan struct{})
	var websocketStartedOnce sync.Once
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	consensusAddress := fixtureConsensusHex

	fixtureResult := func(name string) any {
		var response map[string]any
		if err := json.Unmarshal(loadFixture(t, name), &response); err != nil {
			t.Fatalf("decode fixture %s: %v", name, err)
		}
		return response["result"]
	}
	statusResult := fixtureResult("rpc-status-ok.json")
	writeResult := func(writer http.ResponseWriter, id int, result any) {
		writer.Header().Set("Content-Type", "application/json")
		response := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
		if err := json.NewEncoder(writer).Encode(response); err != nil {
			t.Errorf("encode RPC response: %v", err)
		}
	}
	abciResult := func(queryPath string) any {
		if unquoted, unquoteErr := strconv.Unquote(queryPath); unquoteErr == nil {
			queryPath = unquoted
		}
		switch queryPath {
		case stakingValidatorQuery:
			return fixtureResult("rpc-validator-ok.json")
		case signingInfoQuery:
			return fixtureResult("rpc-signing-info-ok.json")
		case slashingParamsQuery:
			return fixtureResult("rpc-slashing-params-ok.json")
		default:
			t.Errorf("unexpected ABCI query path %q", queryPath)
			return nil
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/":
			var call struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
				Params struct {
					Path string `json:"path"`
				} `json:"params"`
			}
			if decodeErr := json.NewDecoder(request.Body).Decode(&call); decodeErr != nil {
				t.Errorf("decode JSON-RPC request: %v", decodeErr)
				http.Error(writer, "bad request", http.StatusBadRequest)
				return
			}
			switch call.Method {
			case "status":
				writeResult(writer, call.ID, statusResult)
			case "abci_query":
				writeResult(writer, call.ID, abciResult(call.Params.Path))
			default:
				t.Errorf("unexpected JSON-RPC method %q", call.Method)
				http.Error(writer, "unexpected method", http.StatusNotFound)
			}
		case "/status":
			writeResult(writer, -1, statusResult)
		case "/abci_query":
			writeResult(writer, -1, abciResult(request.URL.Query().Get("path")))
		case "/websocket":
			connection, upgradeErr := upgrader.Upgrade(writer, request, nil)
			if upgradeErr != nil {
				t.Errorf("upgrade websocket: %v", upgradeErr)
				return
			}
			defer connection.Close()
			websocketStartedOnce.Do(func() { close(websocketStarted) })
			for range 2 {
				if _, _, readErr := connection.ReadMessage(); readErr != nil {
					return
				}
			}
			for height := int64(1); height <= 200; height++ {
				reply := map[string]any{
					"jsonrpc": "2.0",
					"id":      1,
					"result": map[string]any{
						"query": QueryNewBlock,
						"data": map[string]any{
							"type": "tendermint/event/NewBlock",
							"value": map[string]any{
								"block": map[string]any{
									"header": map[string]string{
										"height":           strconv.FormatInt(height, 10),
										"proposer_address": consensusAddress,
									},
									"last_commit": map[string]any{"signatures": []any{}},
								},
							},
						},
					},
				}
				if writeErr := connection.WriteJSON(reply); writeErr != nil {
					return
				}
			}
			for {
				if _, _, readErr := connection.ReadMessage(); readErr != nil {
					return
				}
			}
		default:
			http.Error(writer, "unexpected path "+request.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	chain := &ChainConfig{
		name:       "state-race",
		ChainId:    chainID,
		ValAddress: validatorAddress,
		Nodes:      []*NodeConfig{{Url: server.URL}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := chain.newRpc(ctx); err != nil {
		t.Fatalf("initial RPC setup: %v", err)
	}
	if err := chain.GetValInfo(ctx, false); err != nil {
		t.Fatalf("initial validator refresh: %v", err)
	}

	websocketDone := make(chan struct{})
	go func() {
		defer close(websocketDone)
		chain.WsRun(ctx)
	}()
	select {
	case <-websocketStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("websocket workload did not start")
	}

	start := make(chan struct{})
	var refreshers sync.WaitGroup
	refreshers.Add(2)
	go func() {
		defer refreshers.Done()
		<-start
		for range 100 {
			if err := chain.newRpc(ctx); err != nil && !strings.Contains(err.Error(), "context canceled") {
				t.Errorf("RPC refresh: %v", err)
				return
			}
		}
	}()
	go func() {
		defer refreshers.Done()
		<-start
		for range 100 {
			if err := chain.GetValInfo(ctx, false); err != nil && !strings.Contains(err.Error(), "context canceled") {
				t.Errorf("validator refresh: %v", err)
				return
			}
		}
	}()
	close(start)
	refreshers.Wait()
	cancel()

	select {
	case <-websocketDone:
	case <-time.After(2 * time.Second):
		t.Fatal("websocket workload did not stop after cancellation")
	}
}
