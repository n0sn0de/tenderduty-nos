package seer

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cosmosed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	slashing "github.com/cosmos/cosmos-sdk/x/slashing/types"
	staking "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/gorilla/websocket"
	abci "github.com/tendermint/tendermint/abci/types"
	tmed25519 "github.com/tendermint/tendermint/crypto/ed25519"
	"github.com/tendermint/tendermint/p2p"
	coretypes "github.com/tendermint/tendermint/rpc/core/types"
	rpctypes "github.com/tendermint/tendermint/rpc/jsonrpc/types"
)

func TestConcurrentRPCValidatorRefreshAndWebSocketWorkloadIsRaceFree(t *testing.T) {
	const chainID = "state-race-1"
	const validatorAddress = "cosmosvaloper1staterefresh"

	websocketStarted := make(chan struct{})
	var websocketStartedOnce sync.Once
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	pubkey := &cosmosed25519.PubKey{Key: []byte("01234567890123456789012345678901")}
	consensusAddress := strings.ToUpper(hex.EncodeToString(pubkey.Address().Bytes()))
	consensusAny, err := codectypes.NewAnyWithValue(pubkey)
	if err != nil {
		t.Fatal(err)
	}
	validatorResponse, err := (&staking.QueryValidatorResponse{Validator: staking.Validator{
		OperatorAddress: validatorAddress,
		ConsensusPubkey: consensusAny,
		Status:          staking.Bonded,
		Description:     staking.Description{Moniker: "state-race-validator"},
	}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	signingResponse, err := (&slashing.QuerySigningInfoResponse{ValSigningInfo: slashing.ValidatorSigningInfo{
		Address:             "cosmosvalcons1staterefresh",
		MissedBlocksCounter: 7,
	}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	paramsResponse, err := (&slashing.QueryParamsResponse{Params: slashing.Params{SignedBlocksWindow: 100}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}

	writeResult := func(writer http.ResponseWriter, id int, result any) {
		writer.Header().Set("Content-Type", "application/json")
		response := rpctypes.NewRPCSuccessResponse(rpctypes.JSONRPCIntID(id), result)
		if err := json.NewEncoder(writer).Encode(response); err != nil {
			t.Errorf("encode RPC response: %v", err)
		}
	}
	abciValue := func(queryPath string) []byte {
		if unquoted, unquoteErr := strconv.Unquote(queryPath); unquoteErr == nil {
			queryPath = unquoted
		}
		switch queryPath {
		case "/cosmos.staking.v1beta1.Query/Validator":
			return validatorResponse
		case "/cosmos.slashing.v1beta1.Query/SigningInfo":
			return signingResponse
		case "/cosmos.slashing.v1beta1.Query/Params":
			return paramsResponse
		default:
			t.Errorf("unexpected ABCI query path %q", queryPath)
			return nil
		}
	}
	statusResult := func() *coretypes.ResultStatus {
		return &coretypes.ResultStatus{
			NodeInfo: p2p.DefaultNodeInfo{Network: chainID},
			SyncInfo: coretypes.SyncInfo{
				LatestBlockHeight:   1,
				LatestBlockTime:     time.Unix(1, 0).UTC(),
				EarliestBlockHeight: 1,
				EarliestBlockTime:   time.Unix(1, 0).UTC(),
				CatchingUp:          false,
			},
			ValidatorInfo: coretypes.ValidatorInfo{PubKey: tmed25519.PubKey(make([]byte, tmed25519.PubKeySize))},
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
				writeResult(writer, call.ID, statusResult())
			case "abci_query":
				writeResult(writer, call.ID, &coretypes.ResultABCIQuery{Response: abci.ResponseQuery{Value: abciValue(call.Params.Path), Height: 1}})
			default:
				t.Errorf("unexpected JSON-RPC method %q", call.Method)
				http.Error(writer, "unexpected method", http.StatusNotFound)
			}
		case "/status":
			writeResult(writer, -1, statusResult())
		case "/abci_query":
			writeResult(writer, -1, &coretypes.ResultABCIQuery{Response: abci.ResponseQuery{Value: abciValue(request.URL.Query().Get("path")), Height: 1}})
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
