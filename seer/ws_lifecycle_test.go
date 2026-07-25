package seer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tendermint/tendermint/rpc/client/http"
)

type countingWebSocketConnection struct {
	closeCalls     atomic.Int32
	writeCalls     atomic.Int32
	closeOnce      sync.Once
	subscribed     chan struct{}
	subscribedOnce sync.Once
	closed         chan struct{}
}

func newCountingWebSocketConnection() *countingWebSocketConnection {
	return &countingWebSocketConnection{
		subscribed: make(chan struct{}),
		closed:     make(chan struct{}),
	}
}

func (connection *countingWebSocketConnection) SetCompressionLevel(int) error {
	return nil
}

func (connection *countingWebSocketConnection) WriteMessage(int, []byte) error {
	if connection.writeCalls.Add(1) == 2 {
		connection.subscribedOnce.Do(func() { close(connection.subscribed) })
	}
	return nil
}

func (connection *countingWebSocketConnection) ReadMessage() (int, []byte, error) {
	<-connection.closed
	return 0, nil, errors.New("closed")
}

func (connection *countingWebSocketConnection) Close() error {
	connection.closeCalls.Add(1)
	connection.closeOnce.Do(func() { close(connection.closed) })
	return nil
}

func TestWsRunCancellationSerializesOnePublishedWebSocketClose(t *testing.T) {
	client, err := http.New("http://127.0.0.1:26657", "/websocket")
	if err != nil {
		t.Fatal(err)
	}
	connection := newCountingWebSocketConnection()
	previousDial := newWebSocketConnection
	newWebSocketConnection = func(context.Context, string, bool) (websocketConnection, error) {
		return connection, nil
	}
	defer func() { newWebSocketConnection = previousDial }()

	chain := &ChainConfig{name: "websocket-close", ChainId: "websocket-close-1"}
	chain.setRPCClient(client)
	chain.publishValidatorInfo(&ValInfo{Conspub: []byte{1, 2, 3}}, false)

	ctx, cancel := context.WithCancel(context.Background())
	wsDone := make(chan struct{})
	go func() {
		defer close(wsDone)
		chain.WsRun(ctx)
	}()

	select {
	case <-connection.subscribed:
	case <-time.After(time.Second):
		t.Fatal("WsRun did not publish both subscriptions")
	}

	start := make(chan struct{})
	var closers sync.WaitGroup
	for range 32 {
		closers.Add(1)
		go func() {
			defer closers.Done()
			<-start
			chain.closeWebSocket()
		}()
	}
	close(start)
	cancel()
	closers.Wait()

	select {
	case <-wsDone:
	case <-time.After(time.Second):
		t.Fatal("WsRun did not stop after cancellation")
	}
	if got := connection.closeCalls.Load(); got != 1 {
		t.Fatalf("websocket Close calls = %d, want exactly 1", got)
	}
	chain.connectionMux.Lock()
	defer chain.connectionMux.Unlock()
	if chain.wsclient != nil {
		t.Fatal("closed websocket remained published")
	}
}
