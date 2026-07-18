package dash

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestWebsocketOriginPolicy(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://seer.example/ws", nil)
	request.Header.Set("Origin", "https://other.example")
	if checkWebsocketOrigin(request) {
		t.Fatal("foreign websocket origin was accepted")
	}
	request.Header.Set("Origin", "https://seer.example")
	if !checkWebsocketOrigin(request) {
		t.Fatal("same-host websocket origin was rejected")
	}
	request.Header.Del("Origin")
	if !checkWebsocketOrigin(request) {
		t.Fatal("non-browser client without Origin was rejected")
	}
}

func TestCacheHandlerSetsOperatorSecurityHeaders(t *testing.T) {
	rootDir = fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("seer")}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)

	CacheHandler{}.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	for header, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "default-src 'self'",
		"X-Application-Name":      "NosNode Seer",
	} {
		if got := recorder.Header().Get(header); !strings.HasPrefix(got, want) {
			t.Errorf("%s = %q, want prefix %q", header, got, want)
		}
	}
	if got := recorder.Header().Get("X-Powered-By"); got != "" {
		t.Errorf("legacy disclosure header remains: %q", got)
	}
}

func TestCacheHandlerServesEmbeddedFS(t *testing.T) {
	var _ fs.FS = rootDir
}
