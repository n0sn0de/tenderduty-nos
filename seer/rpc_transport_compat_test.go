package seer

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	directOriginHelperEnv    = "SEER_TEST_DIRECT_ORIGIN_HELPER"
	directOriginEndpointEnv  = "SEER_TEST_DIRECT_ORIGIN_ENDPOINT"
	directOriginChainID      = "fixture-1"
	directOriginRPCQueryPath = "/"
)

type rpcRequestObservation struct {
	requestURI string
	host       string
	rpcMethod  string
	urlIsAbs   bool
	decodeErr  error
}

func TestCometBFTRPCConfiguredHostnamePreservesLegacyDirectOrigin(t *testing.T) {
	if os.Getenv(directOriginHelperEnv) == "1" {
		runDirectOriginRPCClientHelper(t)
		return
	}

	var statusFixture map[string]any
	if err := json.Unmarshal(loadFixture(t, "rpc-status-ok.json"), &statusFixture); err != nil {
		t.Fatalf("decode status fixture: %v", err)
	}

	tests := []struct {
		name          string
		proxyVariable string
	}{
		{name: "ordinary no-proxy environment"},
		{name: "HTTP proxy configured", proxyVariable: "HTTP_PROXY"},
		{name: "HTTPS proxy configured", proxyVariable: "HTTPS_PROXY"},
		{name: "all-protocol proxy configured", proxyVariable: "ALL_PROXY"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			originRequests := make(chan rpcRequestObservation, 4)
			origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var call struct {
					ID     any    `json:"id"`
					Method string `json:"method"`
				}
				decodeErr := json.NewDecoder(request.Body).Decode(&call)
				originRequests <- rpcRequestObservation{
					requestURI: request.RequestURI,
					host:       request.Host,
					rpcMethod:  call.Method,
					urlIsAbs:   request.URL.IsAbs(),
					decodeErr:  decodeErr,
				}

				response := make(map[string]any, len(statusFixture)+1)
				for key, value := range statusFixture {
					response[key] = value
				}
				response["id"] = call.ID
				writer.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(writer).Encode(response)
			}))
			defer origin.Close()

			proxyRequests := make(chan string, 1)
			proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				proxyRequests <- request.RequestURI
				http.Error(writer, "configured proxy must not receive consensus RPC traffic", http.StatusBadGateway)
			}))
			defer proxy.Close()

			originURL, err := url.Parse(origin.URL)
			if err != nil {
				t.Fatalf("parse origin URL: %v", err)
			}
			endpoint := "http://rpc.seer.test:" + originURL.Port()

			command := exec.Command(os.Args[0], "-test.run=^TestCometBFTRPCConfiguredHostnamePreservesLegacyDirectOrigin$")
			command.Env = append(environmentWithoutProxyVariables(os.Environ()),
				directOriginHelperEnv+"=1",
				directOriginEndpointEnv+"="+endpoint,
				"NO_PROXY=",
				"no_proxy=",
			)
			if test.proxyVariable != "" {
				command.Env = append(command.Env,
					test.proxyVariable+"="+proxy.URL,
					strings.ToLower(test.proxyVariable)+"="+proxy.URL,
				)
			}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("RPC helper failed: %v\n%s", err, output)
			}

			for requestIndex := 0; requestIndex < 2; requestIndex++ {
				select {
				case observation := <-originRequests:
					if observation.decodeErr != nil {
						t.Fatalf("decode origin request %d: %v", requestIndex, observation.decodeErr)
					}
					if observation.requestURI != directOriginRPCQueryPath || observation.urlIsAbs {
						t.Fatalf("origin request %d target = %q (absolute=%t), want origin-form %q", requestIndex, observation.requestURI, observation.urlIsAbs, directOriginRPCQueryPath)
					}
					if observation.host != strings.TrimPrefix(endpoint, "http://") {
						t.Fatalf("origin request %d Host = %q, want %q", requestIndex, observation.host, strings.TrimPrefix(endpoint, "http://"))
					}
					if observation.rpcMethod != "status" {
						t.Fatalf("origin request %d RPC method = %q, want status", requestIndex, observation.rpcMethod)
					}
				case <-time.After(2 * time.Second):
					t.Fatalf("timed out waiting for origin request %d", requestIndex)
				}
			}
			select {
			case requestURI := <-proxyRequests:
				t.Fatalf("configured proxy received consensus RPC request %q", requestURI)
			default:
			}
		})
	}
}

func TestCometBFTRPCStatusHonorsContextCancellationAndDeadline(t *testing.T) {
	tests := []struct {
		name    string
		makeCtx func() (context.Context, context.CancelFunc)
		want    error
		cancel  bool
	}{
		{
			name: "cancellation",
			makeCtx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			want:   context.Canceled,
			cancel: true,
		},
		{
			name: "deadline",
			makeCtx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 50*time.Millisecond)
			},
			want: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestStarted := make(chan struct{})
			releaseHandler := make(chan struct{})
			var started sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				started.Do(func() { close(requestStarted) })
				<-releaseHandler
			}))
			defer func() {
				close(releaseHandler)
				server.Close()
			}()

			client, err := newCometBFTRPCClient(server.URL, "/websocket")
			if err != nil {
				t.Fatalf("create RPC adapter: %v", err)
			}
			ctx, cancel := test.makeCtx()
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := client.Status(ctx)
				result <- err
			}()
			select {
			case <-requestStarted:
			case <-time.After(time.Second):
				t.Fatal("context probe never reached the RPC origin")
			}
			if test.cancel {
				cancel()
			}
			select {
			case err := <-result:
				if !errors.Is(err, test.want) {
					t.Fatalf("status error = %v, want %v", err, test.want)
				}
			case <-time.After(time.Second):
				t.Fatalf("status did not return after %v", test.want)
			}
		})
	}
}

func TestCometBFTRPCFactoryRetainsOrdinaryFallback(t *testing.T) {
	unavailable, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve unavailable endpoint: %v", err)
	}
	unavailableURL := "http://" + unavailable.Addr().String()
	if err := unavailable.Close(); err != nil {
		t.Fatalf("close unavailable endpoint: %v", err)
	}

	healthy := rpcFixtureServer(t, map[string]string{"status": "rpc-status-ok.json"})
	defer healthy.Close()
	failedNode := &NodeConfig{Url: unavailableURL}
	workingNode := &NodeConfig{Url: healthy.URL}
	chain := &ChainConfig{
		name:    "production-factory-fallback",
		ChainId: directOriginChainID,
		Nodes:   []*NodeConfig{failedNode, workingNode},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := chain.newRpc(ctx); err != nil {
		t.Fatalf("select fallback endpoint: %v", err)
	}
	selected := chain.rpcClientSnapshot()
	if selected == nil || selected.Remote() != healthy.URL {
		t.Fatalf("selected RPC client = %v, want %q", selected, healthy.URL)
	}
	if state := chain.nodeState(failedNode); !state.down {
		t.Fatalf("unavailable first endpoint was not marked down: %+v", state)
	}
}

func TestNewCometBFTRPCClientDoesNotMutateSharedDefaultTransport(t *testing.T) {
	sharedTransport := http.DefaultTransport
	defaultTransport, ok := sharedTransport.(*http.Transport)
	if !ok || defaultTransport.Proxy == nil {
		t.Fatalf("unexpected shared default transport %T", sharedTransport)
	}
	server := rpcFixtureServer(t, map[string]string{"status": "rpc-status-ok.json"})
	defer server.Close()
	directClient, err := newCometBFTRPCHTTPClient(server.URL)
	if err != nil {
		t.Fatalf("create direct JSON-RPC HTTP client: %v", err)
	}
	directTransport, ok := directClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected direct transport %T", directClient.Transport)
	}
	if directTransport == defaultTransport || directTransport.Proxy != nil {
		t.Fatalf("direct transport = %p proxy-nil=%t, shared default = %p", directTransport, directTransport.Proxy == nil, defaultTransport)
	}
	if _, err := newCometBFTRPCClient(server.URL, "/websocket"); err != nil {
		t.Fatalf("create RPC adapter: %v", err)
	}
	if http.DefaultTransport != sharedTransport {
		t.Fatal("RPC client construction replaced http.DefaultTransport")
	}
	if defaultTransport.Proxy == nil {
		t.Fatal("RPC client construction cleared the shared default transport proxy")
	}
}

func runDirectOriginRPCClientHelper(t *testing.T) {
	endpoint := os.Getenv(directOriginEndpointEnv)
	if endpoint == "" {
		t.Fatal("missing direct-origin endpoint")
	}
	previousResolver := net.DefaultResolver
	net.DefaultResolver = loopbackTestResolver(t)
	defer func() { net.DefaultResolver = previousResolver }()

	chain := &ChainConfig{
		name:    "direct-origin-helper",
		ChainId: directOriginChainID,
		Nodes:   []*NodeConfig{{Url: endpoint}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupHost(ctx, "rpc.seer.test")
	if err != nil || len(addresses) != 1 || addresses[0] != "127.0.0.1" {
		t.Fatalf("resolve configured test hostname: addresses=%v err=%v", addresses, err)
	}
	if err := chain.newRpc(ctx); err != nil {
		t.Fatalf("select configured hostname endpoint: %v", err)
	}
	client := chain.rpcClientSnapshot()
	if client == nil {
		t.Fatal("configured hostname endpoint did not publish an RPC client")
	}
	status, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("reuse configured hostname endpoint: %v", err)
	}
	if status.Network != directOriginChainID || status.CatchingUp {
		t.Fatalf("configured hostname endpoint status = %+v", status)
	}
}

func environmentWithoutProxyVariables(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "REQUEST_METHOD":
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func loopbackTestResolver(t *testing.T) *net.Resolver {
	t.Helper()
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start test DNS server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	go func() {
		query := make([]byte, 512)
		for {
			read, address, err := server.ReadFrom(query)
			if err != nil {
				return
			}
			response, err := loopbackDNSResponse(query[:read])
			if err != nil {
				continue
			}
			_, _ = server.WriteTo(response, address)
		}
	}()

	resolverAddress := server.LocalAddr().String()
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "udp4", resolverAddress)
		},
	}
}

func loopbackDNSResponse(query []byte) ([]byte, error) {
	if len(query) < 17 {
		return nil, fmt.Errorf("short DNS query: %d bytes", len(query))
	}
	questionEnd := 12
	for {
		if questionEnd >= len(query) {
			return nil, errors.New("unterminated DNS question")
		}
		labelLength := int(query[questionEnd])
		questionEnd++
		if labelLength == 0 {
			break
		}
		questionEnd += labelLength
	}
	if questionEnd+4 > len(query) {
		return nil, errors.New("truncated DNS question")
	}
	queryType := binary.BigEndian.Uint16(query[questionEnd : questionEnd+2])
	questionEnd += 4
	answerCount := uint16(0)
	if queryType == 1 {
		answerCount = 1
	}

	response := make([]byte, 12, 12+(questionEnd-12)+16)
	copy(response[:2], query[:2])
	binary.BigEndian.PutUint16(response[2:4], 0x8180)
	binary.BigEndian.PutUint16(response[4:6], 1)
	binary.BigEndian.PutUint16(response[6:8], answerCount)
	response = append(response, query[12:questionEnd]...)
	if answerCount == 1 {
		response = append(response,
			0xc0, 0x0c,
			0x00, 0x01,
			0x00, 0x01,
			0x00, 0x00, 0x00, 0x00,
			0x00, 0x04,
			127, 0, 0, 1,
		)
	}
	return response, nil
}
