package dash

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/textileio/go-threads/broadcast"
)

var (
	Content embed.FS
	rootDir fs.FS
	rex     = regexp.MustCompile(`\W(https?|tcp|wss?)://.+\w`)
)

const logLength = 256

// Server owns the dashboard HTTP server, status/log cache worker, listener, and
// every upgraded WebSocket connection.
type Server struct {
	ctx      context.Context
	cancel   context.CancelFunc
	updates  <-chan *ChainStatus
	logs     <-chan LogMessage
	hideLogs bool
	root     fs.FS

	broadcaster broadcast.Broadcaster
	cacheMux    sync.RWMutex
	logCache    []byte
	statusCache []byte
	status      map[string]*ChainStatus
	logSlice    []LogMessage

	websocketMux sync.Mutex
	websockets   map[*websocket.Conn]struct{}
	stopping     bool

	httpServer *http.Server
	listener   net.Listener
	workers    sync.WaitGroup
	startOnce  sync.Once
	done       chan error

	shutdownOnce sync.Once
	shutdownErr  error
}

// NewServer builds the existing dashboard routes without opening a listener.
func NewServer(updates <-chan *ChainStatus, logs <-chan LogMessage, hideLogs bool) (*Server, error) {
	staticRoot, err := fs.Sub(Content, "static")
	if err != nil {
		return nil, err
	}
	rootDir = staticRoot
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		ctx:         ctx,
		cancel:      cancel,
		updates:     updates,
		logs:        logs,
		hideLogs:    hideLogs,
		root:        staticRoot,
		logCache:    []byte{'[', ']'},
		statusCache: []byte{'{', '}'},
		status:      make(map[string]*ChainStatus),
		logSlice:    make([]LogMessage, 0),
		websockets:  make(map[*websocket.Conn]struct{}),
		done:        make(chan error, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.serveWebSocket)
	mux.HandleFunc("/logsenabled", s.serveLogsEnabled)
	mux.HandleFunc("/logs", s.serveLogs)
	mux.HandleFunc("/state", s.serveState)
	mux.Handle("/", &CacheHandler{Root: staticRoot})
	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return s.ctx
		},
	}
	return s, nil
}

// Start begins the cache and HTTP workers on an already-owned listener. It is
// synchronous up to goroutine creation so Shutdown cannot race worker setup.
func (s *Server) Start(listener net.Listener) <-chan error {
	s.startOnce.Do(func() {
		s.listener = listener
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			s.runCache()
		}()
		go func() {
			err := s.httpServer.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
				err = nil
			}
			s.done <- err
			close(s.done)
		}()
	})
	return s.done
}

func (s *Server) runCache() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	updatePending := false
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if updatePending {
				s.cacheMux.RLock()
				status := append([]byte(nil), s.statusCache...)
				s.cacheMux.RUnlock()
				_ = s.broadcaster.Send(status)
				updatePending = false
			}
		case update, ok := <-s.updates:
			if !ok {
				s.updates = nil
				continue
			}
			if update == nil {
				continue
			}
			if s.hideLogs && rex.MatchString(update.LastError) {
				copyOfUpdate := *update
				copyOfUpdate.LastError = rex.ReplaceAllString(copyOfUpdate.LastError, " -redacted-")
				update = &copyOfUpdate
			}
			s.status[update.Name] = update
			result := make([]*ChainStatus, 0, len(s.status))
			for _, current := range s.status {
				result = append(result, current)
			}
			sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
			encoded, err := json.Marshal(struct {
				MessageType string `json:"msgType"`
				Status      []*ChainStatus
			}{MessageType: "update", Status: result})
			if err != nil {
				continue
			}
			s.cacheMux.Lock()
			s.statusCache = encoded
			s.cacheMux.Unlock()
			updatePending = true
		case message, ok := <-s.logs:
			if !ok {
				s.logs = nil
				continue
			}
			if s.hideLogs {
				continue
			}
			if len(s.logSlice) >= logLength {
				s.logSlice = append([]LogMessage{message}, s.logSlice[:len(s.logSlice)-1]...)
			} else {
				s.logSlice = append([]LogMessage{message}, s.logSlice...)
			}
			encodedCache, err := json.Marshal(s.logSlice)
			if err != nil {
				continue
			}
			encodedMessage, err := json.Marshal(message)
			if err != nil {
				continue
			}
			s.cacheMux.Lock()
			s.logCache = encodedCache
			s.cacheMux.Unlock()
			_ = s.broadcaster.Send(encodedMessage)
		}
	}
}

func (s *Server) serveWebSocket(writer http.ResponseWriter, request *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin:       checkWebsocketOrigin,
		EnableCompression: true,
	}
	connection, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	if !s.registerWebSocket(connection) {
		_ = connection.Close()
		return
	}
	defer func() {
		s.unregisterWebSocket(connection)
		_ = connection.Close()
		s.workers.Done()
	}()

	subscription := s.broadcaster.Listen()
	defer subscription.Discard()
	for {
		select {
		case <-s.ctx.Done():
			return
		case message, ok := <-subscription.Channel():
			if !ok {
				return
			}
			payload, ok := message.([]byte)
			if !ok {
				continue
			}
			if err := connection.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		}
	}
}

func (s *Server) registerWebSocket(connection *websocket.Conn) bool {
	s.websocketMux.Lock()
	defer s.websocketMux.Unlock()
	if s.stopping {
		return false
	}
	s.workers.Add(1)
	s.websockets[connection] = struct{}{}
	return true
}

func (s *Server) unregisterWebSocket(connection *websocket.Conn) {
	s.websocketMux.Lock()
	delete(s.websockets, connection)
	s.websocketMux.Unlock()
}

func (s *Server) closeWebSockets() {
	s.websocketMux.Lock()
	s.stopping = true
	connections := make([]*websocket.Conn, 0, len(s.websockets))
	for connection := range s.websockets {
		connections = append(connections, connection)
	}
	s.websocketMux.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

func (s *Server) serveLogsEnabled(writer http.ResponseWriter, _ *http.Request) {
	setSecurityHeaders(writer)
	writer.Header().Set("Content-Type", "application/json")
	encoded, _ := json.Marshal(map[string]bool{"enabled": !s.hideLogs})
	_, _ = writer.Write(encoded)
}

func (s *Server) serveLogs(writer http.ResponseWriter, _ *http.Request) {
	setSecurityHeaders(writer)
	writer.Header().Set("Content-Type", "application/json")
	s.cacheMux.RLock()
	cached := append([]byte(nil), s.logCache...)
	s.cacheMux.RUnlock()
	_, _ = writer.Write(cached)
}

func (s *Server) serveState(writer http.ResponseWriter, _ *http.Request) {
	setSecurityHeaders(writer)
	writer.Header().Set("Content-Type", "application/json")
	s.cacheMux.RLock()
	cached := append([]byte(nil), s.statusCache...)
	s.cacheMux.RUnlock()
	_, _ = writer.Write(cached)
}

// Shutdown stops acceptance, cancels the cache worker, explicitly closes
// hijacked WebSockets, gracefully shuts down HTTP, and joins every worker.
func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.cancel()
		s.broadcaster.Discard()
		s.closeWebSockets()
		httpErr := s.httpServer.Shutdown(ctx)
		if s.listener != nil {
			_ = s.listener.Close()
		}
		workersErr := waitGroupContext(ctx, &s.workers)
		s.shutdownErr = errors.Join(httpErr, workersErr)
	})
	return s.shutdownErr
}

// Close releases a prepared listener even when Start was never called.
func (s *Server) Close() error {
	s.cancel()
	s.broadcaster.Discard()
	s.closeWebSockets()
	var errs []error
	if s.listener != nil {
		errs = append(errs, s.listener.Close())
	}
	if s.httpServer != nil {
		errs = append(errs, s.httpServer.Close())
	}
	return errors.Join(errs...)
}

func waitGroupContext(ctx context.Context, group *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func checkWebsocketOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return strings.EqualFold(parsed.Host, request.Host)
}

func setSecurityHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	writer.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Application-Name", "NosNode Seer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
}

// CacheHandler implements the Handler interface with a Cache-Control set on responses.
type CacheHandler struct {
	Root fs.FS
}

func (handler CacheHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	setSecurityHeaders(writer)
	staticRoot := handler.Root
	if staticRoot == nil {
		staticRoot = rootDir
	}
	http.FileServer(http.FS(staticRoot)).ServeHTTP(writer, request)
}
