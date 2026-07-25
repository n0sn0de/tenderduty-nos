package seer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	fixtureConsensusHex     = "861009EC4D599FAB1F40ABC76E6F89880CFF5833"
	fixtureSecpConsensusHex = "751E76E8199196D454941C45D1B3A323F1433BD6"
	fixtureValoper          = "cosmosvaloper1zy3rx3z4vemc3xgq42aueh0wluqpzg3nyrhpag"
	fixtureValcons          = "cosmosvalcons1scgqnmzdtx06k86q40rkumuf3qx07kpn2ul24p"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func rpcFixtureServer(t *testing.T, fixtures map[string]string) *httptest.Server {
	return rpcFixtureServerExpecting(t, fixtures, nil)
}

func rpcFixtureServerExpecting(t *testing.T, fixtures, expectedQueryData map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := strings.TrimPrefix(request.URL.Path, "/")
		queryPath := request.URL.Query().Get("path")
		queryData := request.URL.Query().Get("data")
		var requestID any = -1
		if request.URL.Path == "/" {
			var call struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
				Params struct {
					Path string `json:"path"`
					Data string `json:"data"`
				} `json:"params"`
			}
			if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
				t.Errorf("decode RPC request: %v", err)
				http.Error(writer, "bad request", http.StatusBadRequest)
				return
			}
			method = call.Method
			queryPath = call.Params.Path
			queryData = call.Params.Data
			requestID = call.ID
		}
		if unquoted, err := strconv.Unquote(queryPath); err == nil {
			queryPath = unquoted
		}
		key := method
		if method == "abci_query" {
			key += ":" + queryPath
		}
		fixtureName, ok := fixtures[key]
		if !ok {
			t.Errorf("unexpected RPC request %q", key)
			http.Error(writer, "unexpected RPC request", http.StatusNotFound)
			return
		}
		if expected, ok := expectedQueryData[key]; ok && queryData != expected {
			t.Errorf("RPC request %q data = %q, want %q", key, queryData, expected)
			http.Error(writer, "unexpected RPC query data", http.StatusBadRequest)
			return
		}
		var response map[string]any
		if err := json.Unmarshal(loadFixture(t, fixtureName), &response); err != nil {
			t.Errorf("decode fixture %s: %v", fixtureName, err)
			http.Error(writer, "bad fixture", http.StatusInternalServerError)
			return
		}
		response["id"] = requestID
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(response); err != nil {
			t.Errorf("encode fixture response: %v", err)
		}
	}))
}

func newFixtureClient(t *testing.T, fixtures map[string]string) rpcClient {
	t.Helper()
	server := rpcFixtureServer(t, fixtures)
	t.Cleanup(server.Close)
	client, err := newCometBFTRPCClient(server.URL, "/websocket")
	if err != nil {
		t.Fatalf("create RPC adapter: %v", err)
	}
	return client
}

type stubRPCClient struct {
	status rpcStatus
	remote string
	quit   chan struct{}
}

func (client *stubRPCClient) Status(context.Context) (rpcStatus, error) {
	return client.status, nil
}
func (*stubRPCClient) Validator(context.Context, string) (validatorRecord, error) {
	return validatorRecord{}, nil
}
func (*stubRPCClient) SigningInfo(context.Context, string) (signingInfo, error) {
	return signingInfo{}, nil
}
func (*stubRPCClient) SlashingParams(context.Context) (slashingParams, error) {
	return slashingParams{}, nil
}
func (client *stubRPCClient) Remote() string        { return client.remote }
func (client *stubRPCClient) Quit() <-chan struct{} { return client.quit }

type recordingRPCFactory struct {
	client         rpcClient
	endpoint, path string
}

func (factory *recordingRPCFactory) New(endpoint, path string) (rpcClient, error) {
	factory.endpoint, factory.path = endpoint, path
	return factory.client, nil
}

type scriptedStatusRPCClient struct {
	stubRPCClient
	statusErr error
	contexts  *[]context.Context
}

func (client *scriptedStatusRPCClient) Status(ctx context.Context) (rpcStatus, error) {
	*client.contexts = append(*client.contexts, ctx)
	return client.status, client.statusErr
}

type scriptedRPCFactory struct {
	order   []string
	clients map[string]*scriptedStatusRPCClient
}

func (factory *scriptedRPCFactory) New(endpoint, _ string) (rpcClient, error) {
	factory.order = append(factory.order, endpoint)
	client, ok := factory.clients[endpoint]
	if !ok {
		return nil, errors.New("unexpected endpoint " + endpoint)
	}
	return client, nil
}

type recordingAddressCodec struct {
	prefix string
	bytes  []byte
}

func (codec *recordingAddressCodec) Encode(prefix string, address []byte) (string, error) {
	codec.prefix = prefix
	codec.bytes = append([]byte(nil), address...)
	return "fixture-consensus-address", nil
}

func TestFirstPartyDependencySeamInjection(t *testing.T) {
	client := &stubRPCClient{status: rpcStatus{Network: "fixture-1"}, remote: "fixture://rpc", quit: make(chan struct{})}
	factory := &recordingRPCFactory{client: client}
	codec := &recordingAddressCodec{}
	chain := &ChainConfig{
		name:          "fixture",
		ChainId:       "fixture-1",
		Nodes:         []*NodeConfig{{Url: "fixture://rpc"}},
		clientFactory: factory,
		addressCodec:  codec,
	}
	if err := chain.newRpc(context.Background()); err != nil {
		t.Fatalf("newRpc through first-party factory: %v", err)
	}
	gotClient, _, _ := chain.monitoringSnapshot()
	if gotClient != client || factory.endpoint != "fixture://rpc" || factory.path != "/websocket" {
		t.Fatalf("factory call = endpoint %q path %q client %T", factory.endpoint, factory.path, gotClient)
	}
	encoded, err := chain.encodeConsensusAddress("fixturevalcons", []byte{0x01, 0x02})
	if err != nil || encoded != "fixture-consensus-address" || codec.prefix != "fixturevalcons" || !bytes.Equal(codec.bytes, []byte{0x01, 0x02}) {
		t.Fatalf("codec result = %q prefix = %q bytes = %X err = %v", encoded, codec.prefix, codec.bytes, err)
	}
}

func TestNewRPCPreservesOrderedFallbackAndSharedDeadline(t *testing.T) {
	const (
		transport = "fixture://transport"
		wrong     = "fixture://wrong-network"
		syncing   = "fixture://catching-up"
		healthy   = "fixture://healthy"
	)
	var contexts []context.Context
	factory := &scriptedRPCFactory{clients: map[string]*scriptedStatusRPCClient{
		transport: {stubRPCClient: stubRPCClient{remote: transport}, statusErr: errors.New("transport failure"), contexts: &contexts},
		wrong:     {stubRPCClient: stubRPCClient{status: rpcStatus{Network: "other-1"}, remote: wrong}, contexts: &contexts},
		syncing:   {stubRPCClient: stubRPCClient{status: rpcStatus{Network: "fixture-1", CatchingUp: true}, remote: syncing}, contexts: &contexts},
		healthy:   {stubRPCClient: stubRPCClient{status: rpcStatus{Network: "fixture-1"}, remote: healthy}, contexts: &contexts},
	}}
	nodes := []*NodeConfig{{Url: transport}, {Url: wrong}, {Url: syncing}, {Url: healthy}}
	chain := &ChainConfig{name: "ordered-fallback", ChainId: "fixture-1", Nodes: nodes, clientFactory: factory}

	if err := chain.newRpc(context.Background()); err != nil {
		t.Fatalf("select healthy endpoint: %v", err)
	}
	if got, want := strings.Join(factory.order, ","), strings.Join([]string{transport, wrong, syncing, healthy}, ","); got != want {
		t.Fatalf("first endpoint order = %q, want %q", got, want)
	}
	if len(contexts) != 4 {
		t.Fatalf("status contexts = %d, want 4", len(contexts))
	}
	deadline, ok := contexts[0].Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 10*time.Second {
		t.Fatalf("selection deadline = %v, ok = %t", deadline, ok)
	}
	for index, ctx := range contexts[1:] {
		if ctx != contexts[0] {
			t.Fatalf("status context %d did not share the selection budget", index+1)
		}
	}
	for index, node := range nodes[:3] {
		state := chain.nodeState(node)
		if !state.down {
			t.Fatalf("failed node %d was not marked down: %+v", index, state)
		}
	}
	if state := chain.nodeState(nodes[2]); !state.syncing {
		t.Fatalf("catching-up node state = %+v", state)
	}
	selected := chain.rpcClientSnapshot()
	if selected == nil || selected.Remote() != healthy {
		t.Fatalf("selected client = %v", selected)
	}

	factory.order = nil
	contexts = nil
	if err := chain.newRpc(context.Background()); err != nil {
		t.Fatalf("reselect known-healthy endpoint: %v", err)
	}
	if got := strings.Join(factory.order, ","); got != healthy {
		t.Fatalf("second endpoint order = %q, want only %q", got, healthy)
	}
	if len(contexts) != 1 {
		t.Fatalf("second status calls = %d, want 1", len(contexts))
	}
}

func TestNewRPCPropagatesParentCancellation(t *testing.T) {
	const endpoint = "fixture://cancelled"
	var contexts []context.Context
	factory := &scriptedRPCFactory{clients: map[string]*scriptedStatusRPCClient{
		endpoint: {stubRPCClient: stubRPCClient{remote: endpoint}, statusErr: context.Canceled, contexts: &contexts},
	}}
	chain := &ChainConfig{
		name:          "cancelled",
		ChainId:       "fixture-1",
		Nodes:         []*NodeConfig{{Url: endpoint}},
		clientFactory: factory,
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	if err := chain.newRpc(parent); err == nil {
		t.Fatal("cancelled selection succeeded")
	}
	if len(contexts) != 1 {
		t.Fatalf("received contexts = %d, want 1", len(contexts))
	}
	if !errors.Is(contexts[0].Err(), context.Canceled) {
		t.Fatalf("received context error = %v, want cancellation", contexts[0].Err())
	}
}

func TestCometBFTRPCStatusFixtures(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		client := newFixtureClient(t, map[string]string{"status": "rpc-status-ok.json"})
		status, err := client.Status(context.Background())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.Network != "fixture-1" || status.CatchingUp {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("catching up", func(t *testing.T) {
		client := newFixtureClient(t, map[string]string{"status": "rpc-status-catching-up.json"})
		status, err := client.Status(context.Background())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.Network != "fixture-1" || !status.CatchingUp {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("missing fields retain zero-value semantics", func(t *testing.T) {
		client := newFixtureClient(t, map[string]string{"status": "rpc-status-missing.json"})
		status, err := client.Status(context.Background())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.Network != "" || status.CatchingUp {
			t.Fatalf("status = %+v, want zero values", status)
		}
	})

	t.Run("RPC error", func(t *testing.T) {
		client := newFixtureClient(t, map[string]string{"status": "rpc-status-error.json"})
		if _, err := client.Status(context.Background()); err == nil {
			t.Fatal("status error fixture succeeded")
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"jsonrpc":`))
		}))
		defer server.Close()
		client, err := newCometBFTRPCClient(server.URL, "/websocket")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Status(context.Background()); err == nil {
			t.Fatal("malformed status fixture succeeded")
		}
	})
}

func TestNewRPCPreservesWrongNetworkAndErrorPaths(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		chainID     string
		messagePart string
		syncing     bool
	}{
		{name: "wrong network", fixture: "rpc-status-ok.json", chainID: "other-1", messagePart: "does not match"},
		{name: "RPC error", fixture: "rpc-status-error.json", chainID: "fixture-1", messagePart: "could not get status"},
		{name: "catching up", fixture: "rpc-status-catching-up.json", chainID: "fixture-1", messagePart: "not synced", syncing: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := rpcFixtureServer(t, map[string]string{"status": test.fixture})
			defer server.Close()
			node := &NodeConfig{Url: server.URL}
			chain := &ChainConfig{name: test.name, ChainId: test.chainID, Nodes: []*NodeConfig{node}}
			if err := chain.newRpc(context.Background()); err == nil {
				t.Fatal("newRpc succeeded")
			}
			state := chain.nodeState(node)
			if !state.down || state.syncing != test.syncing || !strings.Contains(state.lastMsg, test.messagePart) {
				t.Fatalf("node state = %+v", state)
			}
			if !chain.noNodesState() {
				t.Fatal("chain did not retain no-nodes state")
			}
		})
	}
}

func TestValidatorLookupAndAddressNormalizationFixtures(t *testing.T) {
	fixtures := map[string]string{
		"abci_query:/cosmos.staking.v1beta1.Query/Validator":    "rpc-validator-ed25519-ok.json",
		"abci_query:/cosmos.slashing.v1beta1.Query/SigningInfo": "rpc-signing-info-ok.json",
		"abci_query:/cosmos.slashing.v1beta1.Query/Params":      "rpc-slashing-params-ok.json",
	}
	server := rpcFixtureServerExpecting(t, fixtures, map[string]string{
		"abci_query:/cosmos.staking.v1beta1.Query/Validator":    "0A34636F736D6F7376616C6F706572317A79337278337A3476656D633378677134326175656830776C7571707A67336E797268706167",
		"abci_query:/cosmos.slashing.v1beta1.Query/SigningInfo": "0A34636F736D6F7376616C636F6E7331736367716E6D7A64747830366B3836713430726B756D756633717830376B706E32756C323470",
		"abci_query:/cosmos.slashing.v1beta1.Query/Params":      "",
	})
	defer server.Close()
	client, err := newCometBFTRPCClient(server.URL, "/websocket")
	if err != nil {
		t.Fatalf("create RPC adapter: %v", err)
	}

	validator, err := client.Validator(context.Background(), fixtureValoper)
	if err != nil {
		t.Fatalf("validator lookup: %v", err)
	}
	if validator.Moniker != "fixture-validator" || !validator.Jailed || !validator.Bonded {
		t.Fatalf("validator = %+v", validator)
	}
	if got := normalizeConsensusAddress(validator.ConsensusAddress); got != fixtureConsensusHex {
		t.Fatalf("normalized address = %q", got)
	}

	valcons, err := client.Validator(context.Background(), fixtureValcons)
	if err != nil {
		t.Fatalf("valcons lookup: %v", err)
	}
	if got := normalizeConsensusAddress(valcons.ConsensusAddress); got != fixtureConsensusHex {
		t.Fatalf("valcons normalized address = %q", got)
	}
	if valcons.Moniker == "" || !valcons.Bonded {
		t.Fatalf("valcons result = %+v", valcons)
	}

	chain := &ChainConfig{name: "fixture", ChainId: "fixture-1", ValAddress: fixtureValoper}
	chain.setRPCClient(client)
	if err := chain.GetValInfo(context.Background(), false); err != nil {
		t.Fatalf("refresh validator info: %v", err)
	}
	info, _ := chain.validatorInfoSnapshot()
	if info == nil {
		t.Fatal("validator info was not published")
	}
	if info.Valcons != fixtureValcons || info.Missed != 7 || info.Window != 100 {
		t.Fatalf("validator info = %+v", info)
	}
}

func TestValidatorKeyAlgorithmFixtures(t *testing.T) {
	tests := []struct {
		name             string
		fixture          string
		consensusAddress string
	}{
		{name: "Ed25519", fixture: "rpc-validator-ed25519-ok.json", consensusAddress: fixtureConsensusHex},
		{name: "secp256k1", fixture: "rpc-validator-secp256k1-ok.json", consensusAddress: fixtureSecpConsensusHex},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newFixtureClient(t, map[string]string{
				"abci_query:/cosmos.staking.v1beta1.Query/Validator": test.fixture,
			})
			validator, err := client.Validator(context.Background(), fixtureValoper)
			if err != nil {
				t.Fatalf("validator lookup: %v", err)
			}
			if validator.Moniker != "fixture-validator" || !validator.Jailed || !validator.Bonded {
				t.Fatalf("validator = %+v", validator)
			}
			if got := normalizeConsensusAddress(validator.ConsensusAddress); got != test.consensusAddress {
				t.Fatalf("normalized consensus address = %q, want %q", got, test.consensusAddress)
			}
		})
	}
}

func TestValidatorLookupMalformedAndMissingValues(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		messagePart string
	}{
		{name: "missing is a non-validator", fixture: "rpc-validator-missing.json", messagePart: "could not find validator"},
		{name: "malformed protobuf", fixture: "rpc-validator-malformed.json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newFixtureClient(t, map[string]string{
				"abci_query:/cosmos.staking.v1beta1.Query/Validator": test.fixture,
			})
			if _, err := client.Validator(context.Background(), fixtureValoper); err == nil || (test.messagePart != "" && !strings.Contains(err.Error(), test.messagePart)) {
				t.Fatalf("validator error = %v", err)
			}
		})
	}
}

func websocketFixture(t *testing.T, name string) WsReply {
	t.Helper()
	var reply WsReply
	if err := json.Unmarshal(loadFixture(t, name), &reply); err != nil {
		t.Fatalf("decode websocket fixture %s: %v", name, err)
	}
	return reply
}

func TestBlockEventFixtureConversion(t *testing.T) {
	reply := websocketFixture(t, "websocket-new-block.json")
	event, err := decodeBlockEvent(reply.Value())
	if err != nil {
		t.Fatalf("decode block: %v", err)
	}
	if event.Height != 42 || event.ProposerAddress != strings.Repeat("A", 40) || len(event.ValidatorAddresses) != 2 {
		t.Fatalf("block event = %+v", event)
	}

	tests := []struct {
		name    string
		address string
		status  StatusType
	}{
		{name: "proposed", address: strings.Repeat("A", 40), status: StatusProposed},
		{name: "signed", address: fixtureConsensusHex, status: StatusSigned},
		{name: "missed", address: strings.Repeat("C", 40), status: Statusmissed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			update := classifyBlockEvent(event, test.address)
			if update.Height != 42 || update.Status != test.status || !update.Final {
				t.Fatalf("update = %+v", update)
			}
		})
	}

	missing := websocketFixture(t, "websocket-new-block-missing.json")
	missingEvent, err := decodeBlockEvent(missing.Value())
	if err != nil {
		t.Fatalf("decode missing block fields: %v", err)
	}
	if update := classifyBlockEvent(missingEvent, fixtureConsensusHex); update.Height != 0 || update.Status != Statusmissed || !update.Final {
		t.Fatalf("missing-field update = %+v", update)
	}
	if _, err := decodeBlockEvent([]byte("{")); err == nil {
		t.Fatal("malformed block event succeeded")
	}
}

func TestVoteEventFixtureConversion(t *testing.T) {
	reply := websocketFixture(t, "websocket-vote-prevote.json")
	event, err := decodeVoteEvent(reply.Value())
	if err != nil {
		t.Fatalf("decode vote: %v", err)
	}
	tests := []struct {
		name   string
		kind   voteType
		status StatusType
	}{
		{name: "prevote", kind: voteTypePrevote, status: StatusPrevote},
		{name: "precommit", kind: voteTypePrecommit, status: StatusPrecommit},
		{name: "proposal", kind: voteTypeProposal, status: StatusProposed},
		{name: "unknown retains zero-value status", kind: voteType(99), status: Statusmissed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event.Type = test.kind
			update, ok := classifyVoteEvent(event, fixtureConsensusHex)
			if !ok || update.Height != 43 || update.Status != test.status || update.Final {
				t.Fatalf("vote update = %+v, ok = %v", update, ok)
			}
		})
	}
	if _, ok := classifyVoteEvent(event, strings.Repeat("F", 40)); ok {
		t.Fatal("non-validator vote produced an update")
	}

	missing := websocketFixture(t, "websocket-vote-missing.json")
	missingEvent, err := decodeVoteEvent(missing.Value())
	if err != nil {
		t.Fatalf("decode missing vote fields: %v", err)
	}
	missingUpdate, ok := classifyVoteEvent(missingEvent, "")
	if !ok || missingUpdate.Height != 0 || missingUpdate.Status != Statusmissed || missingUpdate.Final {
		t.Fatalf("missing-field vote update = %+v, ok = %v", missingUpdate, ok)
	}
	invalidHeightPayload := bytes.Replace(reply.Value(), []byte(`"height": "43"`), []byte(`"height": "not-a-number"`), 1)
	invalidHeightEvent, err := decodeVoteEvent(invalidHeightPayload)
	if err != nil || invalidHeightEvent.Height != 0 {
		t.Fatalf("invalid-height vote event = %+v, err = %v", invalidHeightEvent, err)
	}
	if _, err := decodeVoteEvent([]byte("{")); err == nil {
		t.Fatal("malformed vote event succeeded")
	}
}

func TestNormalizeConsensusAddressCopiesBytesToUpperHex(t *testing.T) {
	address := []byte{0x00, 0xab, 0xcd, 0xef}
	if got := normalizeConsensusAddress(address); got != "00ABCDEF" {
		t.Fatalf("normalized address = %q", got)
	}
	if !bytes.Equal(address, []byte{0x00, 0xab, 0xcd, 0xef}) {
		t.Fatalf("normalization mutated input: %X", address)
	}
}

func TestToBytesRetainsLegacyExportedCompatibility(t *testing.T) {
	if got := ToBytes("00AbCdEf"); !bytes.Equal(got, []byte{0x00, 0xab, 0xcd, 0xef}) {
		t.Fatalf("ToBytes = %X", got)
	}
}
