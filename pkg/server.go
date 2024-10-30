package pkg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"
	"golang.org/x/crypto/ssh"
)

type Server struct {
	logger       *log.Logger
	cAddr        string
	sshCfg       *ssh.ServerConfig
	sessions     map[string]*Session
	mu           sync.Mutex
	shuttingDown atomic.Bool
	clLn         net.Listener
}

func NewServer(cAddr string, logger *log.Logger) (*Server, error) {
	// Todo: how to properly read/create a key?
	privateKeyBytes, err := os.ReadFile(filepath.Join(os.Getenv("KEYS_DIR") + "\\id_ed25519"))
	if err != nil {
		return nil, fmt.Errorf("failed to read private key, got: %s", err)
	}

	privateKey, err := ssh.ParsePrivateKey(privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key bytes, got: %s", err)
	}

	// Todo: sensible defaults
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			logger.Debug("password callback", "user", c.User(), "password", string(pass))
			return nil, nil
		},
	}
	config.AddHostKey(privateKey)

	return &Server{
		cAddr:    cAddr,
		logger:   logger,
		sshCfg:   config,
		sessions: make(map[string]*Session, 0),
	}, nil
}

func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cAddr)
	if err != nil {
		log.Fatal("failed to listen on port 9000:", err)
	}
	defer ln.Close()

	s.clLn = ln

	go func() {
		for {
			netConn, err := s.clLn.Accept()
			if err != nil {
				if s.shuttingDown.Load() {
					s.logger.Info("stopping client listener accept")
				}

				s.logger.Error("failed to accept new network conn", "err", err)
				return
			}

			s.logger.Info("Accepted net connection", "raddr", netConn.RemoteAddr(), "laddr", netConn.LocalAddr())

			sshConn, chans, reqs, err := ssh.NewServerConn(netConn, s.sshCfg)
			if err != nil {
				s.logger.Error("failed to create server conn", "err", err)
				return
			}

			subdomain := generateSubdomain()
			logger := s.logger.WithPrefix(subdomain)
			sesh, err := newSession(ctx, logger, subdomain, sshConn, chans, reqs)
			if err != nil {
				s.logger.Error("failed to create new session", "err", err)
				return
			}

			s.logger.Info("Session created", "subdomain", subdomain)

			s.mu.Lock()
			s.sessions[sesh.subdomain] = sesh
			s.mu.Unlock()
		}
	}()

	server := s.newServerProxy()

	go func() {
		s.logger.Info("Serving")
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Fatal(err)
		}
	}()

	<-ctx.Done()

	ctxShutDown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.logger.Info("Shutting down")
	err = server.Shutdown(ctxShutDown)
	if err != nil {
		return fmt.Errorf("err on shutdown, got: %s", err)
	}

	return nil
}

func (s *Server) Close(ctx context.Context) error {
	s.shuttingDown.Store(true)
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.clLn.Close()
	if err != nil {
		s.logger.Error("client listener close", "err", err)
	}

	return nil
}

type ctxKey string

const (
	SessionContextKey ctxKey = "session"
)

func (s *Server) newServerProxy() *http.Server {
	proxyHandler := &httputil.ReverseProxy{
		Transport: &http.Transport{
			DisableKeepAlives: false,

			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				s.logger.Info("Dialing", "addr", addr)

				sesh, ok := ctx.Value(SessionContextKey).(*Session)
				if !ok {
					s.logger.Fatal("DialContext: Invalid session object in context", "network", network, "addr", addr)
				}

				t, err := sesh.Accept(ctx)
				if err != nil {
					s.logger.Error("Failed on accept tunnel", "addr", addr, "err", err)
					return nil, err
				}
				s.logger.Debug("Accepted tunnel", "addr", addr)
				return t, nil
			},
			Proxy: http.ProxyFromEnvironment,
		},
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetXForwarded()

			s.logger.Info("Rewriting", "method", r.In.Method, "path", r.In.Host+r.In.URL.String())

			sesh, ok := r.In.Context().Value(SessionContextKey).(*Session)
			if !ok {
				s.logger.Fatal("Rewrite: Invalid session object in context", "proto", r.In.Proto, "method", r.In.Method, "host", r.In.Host, "path", r.In.URL.String())
			}

			url, _ := url.Parse(fmt.Sprintf("http://%s", sesh.subdomain))
			r.SetURL(url)

			trace := &httptrace.ClientTrace{
				ConnectDone: func(network, addr string, err error) {
					s.logger.Debug("Dial complete", "network", network, "addr", addr, "err", err)
				},
				GetConn: func(hostPort string) {
					s.logger.Debug("GetConn", "hostPort", hostPort)
				},
				GotConn: func(info httptrace.GotConnInfo) {
					s.logger.Debug("GotConn", "reused", info.Reused, "wasIdle", info.WasIdle)
				},
				PutIdleConn: func(err error) {
					if err != nil {
						s.logger.Debug("Failed to return conn to pool", "err", err)
					} else {
						s.logger.Error("Conn was returned to pool")
					}
				},
			}
			r.Out = r.Out.WithContext(httptrace.WithClientTrace(r.Out.Context(), trace))
		},
		ErrorLog: s.logger.StandardLog(),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.logger.Error("http: proxy error", "path", r.URL.Path, "err", err)
			w.WriteHeader(http.StatusBadGateway)

		},
		ModifyResponse: func(r *http.Response) error {
			s.logger.Info("routing back response", "req", r.Request.URL, "status", r.Status, "content-length", r.ContentLength)
			return nil
		},
	}

	mux := http.ServeMux{}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		upgrade := r.Header.Get("Upgrade")
		if upgrade == "websocket" {
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}

		subpart, _, _ := strings.Cut(r.Host, ".")
		s.mu.Lock()
		sesh, found := s.sessions[subpart]
		s.mu.Unlock()
		if !found {
			s.logger.Warn("Could not find session for incoming request", "host", r.Host, "subpart", subpart)
			w.WriteHeader(http.StatusNotFound)
			w.Write(nil)
			return
		}

		ctx := context.WithValue(r.Context(), SessionContextKey, sesh)
		r = r.WithContext(ctx)
		s.logger.Info("Serving request", "proto", r.Proto, "path", r.URL.Path)

		// idea: can create middleware to manage notif chans for different proxy backends/conns
		proxyHandler.ServeHTTP(w, r)
	})

	server := &http.Server{
		Addr:         "127.0.0.1:9997",
		Handler:      &mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		ConnState: func(conn net.Conn, state http.ConnState) {
			log.Debug("Conn state changed", "state", state.String())
		},
	}

	return server
}
