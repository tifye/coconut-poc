package e2etest

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"time"
)

type MockBackend struct {
	Server   *httptest.Server
	Listener net.Listener
	Addr     string

	rateLimit   uint
	rateLimiter *time.Ticker
	latency     uint
}

type MockBackendConfig struct {
	RateLimit uint
	Latency   uint
}

func newMockBackend(config *MockBackendConfig) *MockBackend {
	backend := &MockBackend{
		rateLimit: config.RateLimit,
		latency:   config.Latency,
	}

	if backend.rateLimit > 0 {
		backend.rateLimiter = time.NewTicker(time.Second / time.Duration(backend.rateLimit))
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(backend.handleRequest))
	backend.Server = server

	server.Start()
	backend.Addr = server.Listener.Addr().String()

	return backend
}

func (mb *MockBackend) Close() {
	if mb.rateLimiter != nil {
		mb.rateLimiter.Stop()
	}
	mb.Server.CloseClientConnections()
	mb.Server.Close()
}

func (mb *MockBackend) handleRequest(w http.ResponseWriter, r *http.Request) {
	if mb.rateLimiter != nil {
		select {
		case <-mb.rateLimiter.C:
		default:
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
	}

	if mb.latency > 0 {
		timer := time.NewTimer(time.Millisecond * time.Duration(mb.latency))
		<-timer.C
	}

	switch r.URL.Path {
	case "/echo":
		mb.handleEcho(w, r)
	case "/echo/body":
		mb.handleEchoBody(w, r)
	default:
		mb.handleDefault(w, r)
	}
}

func (mb *MockBackend) handleEchoBody(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, err = w.Write(body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func (mb *MockBackend) handleEcho(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"method":  r.Method,
		"path":    r.URL.Path,
		"headers": r.Header,
		"query":   r.URL.Query(),
	}

	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			response["body"] = string(body)
		}
	}

	err := json.NewEncoder(w).Encode(response)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func (mb *MockBackend) handleDefault(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	response := map[string]string{
		"message": "mock backend response",
		"path":    r.URL.Path,
	}
	err := json.NewEncoder(w).Encode(response)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}
