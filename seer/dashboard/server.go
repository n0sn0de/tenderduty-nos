package dash

import (
	"embed"
	"encoding/json"
	"github.com/gorilla/websocket"
	"github.com/textileio/go-threads/broadcast"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	Content embed.FS
	rootDir fs.FS
	rex     = regexp.MustCompile(`\W(https?|tcp|wss?)://.+\w`)
)

const logLength = 256

func Serve(port string, updates chan *ChainStatus, logs chan LogMessage, hideLogs bool) {
	var err error
	rootDir, err = fs.Sub(Content, "static")
	if err != nil {
		log.Fatalln(err)
	}
	var cast broadcast.Broadcaster

	// cache the json .... don't serialize on-demand
	logCache, statusCache := []byte{'[', ']'}, []byte{'{', '}'}

	statusMux := sync.Mutex{}
	status := make(map[string]*ChainStatus)
	logSlice := make([]LogMessage, 0)

	type statusUpdate struct {
		MessageType string `json:"msgType"`
		Status      []*ChainStatus
	}

	go func() {
		tick := time.NewTicker(time.Second)
		update := false
		for {
			select {
			case <-tick.C:
				if update {
					_ = cast.Send(statusCache)
					update = false
				}

			case u := <-updates:
				// try to catch any accidental rpc endpoint leaks
				if hideLogs && rex.MatchString(u.LastError) {
					u.LastError = rex.ReplaceAllString(u.LastError, " -redacted-")
				}
				statusMux.Lock() // probably unnecessary
				status[u.Name] = u
				result := make([]*ChainStatus, 0)
				for k := range status {
					result = append(result, status[k])
				}
				statusMux.Unlock()
				sort.Slice(result, func(i, j int) bool {
					return sort.StringsAreSorted([]string{result[i].Name, result[j].Name})
				})
				j, e := json.Marshal(statusUpdate{
					MessageType: "update",
					Status:      result,
				})
				if e != nil {
					continue
				}
				statusCache = j
				update = true

			case l := <-logs:
				if hideLogs {
					continue
				}
				if len(logSlice) >= logLength {
					logSlice = append([]LogMessage{l}, logSlice[0:len(logSlice)-1]...)
				} else {
					logSlice = append([]LogMessage{l}, logSlice...)
				}
				j, e := json.Marshal(logSlice)
				if e != nil {
					continue
				}
				logCache = j
				j, e = json.Marshal(l)
				if e != nil {
					continue
				}
				_ = cast.Send(j)
			}
		}
	}()

	upgrader := websocket.Upgrader{
		CheckOrigin:       checkWebsocketOrigin,
		EnableCompression: true,
	}
	mux := http.NewServeMux()

	mux.HandleFunc("/ws", func(writer http.ResponseWriter, request *http.Request) {
		c, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer c.Close()
		sub := cast.Listen()
		defer sub.Discard()
		for message := range sub.Channel() {
			e := c.WriteMessage(websocket.TextMessage, message.([]byte))
			if e != nil {
				return
			}
		}
	})

	mux.HandleFunc("/logsenabled", func(writer http.ResponseWriter, request *http.Request) {
		setSecurityHeaders(writer)
		writer.Header().Set("Content-Type", "application/json")
		j, _ := json.Marshal(map[string]bool{"enabled": !hideLogs})
		_, _ = writer.Write(j)
	})

	mux.HandleFunc("/logs", func(writer http.ResponseWriter, request *http.Request) {
		setSecurityHeaders(writer)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(logCache)
	})

	mux.HandleFunc("/state", func(writer http.ResponseWriter, request *http.Request) {
		setSecurityHeaders(writer)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(statusCache)
	})

	mux.Handle("/", &CacheHandler{})
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}
	err = server.ListenAndServe()
	cast.Discard()
	log.Fatal("NosNode Seer dashboard server failed", err)
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

// CacheHandler implements the Handler interface with a Cache-Control set on responses
type CacheHandler struct{}

func (ch CacheHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	setSecurityHeaders(writer)
	http.FileServer(http.FS(rootDir)).ServeHTTP(writer, request)
}
