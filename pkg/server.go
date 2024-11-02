package pkg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"
	"golang.org/x/crypto/ssh"
)

type ServerConfig struct {
	Signer                  ssh.Signer
	PublicKeyAuthAlgorithms []string
	PublicKeyCallback       func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error)
	ClientListenerAddress   string
	NoClientAuth            bool
	Logger                  *log.Logger
}

type Server struct {
	logger       *log.Logger
	cAddr        string
	sshcfg       *ssh.ServerConfig
	sessions     map[string]*Session
	mu           sync.Mutex
	shuttingDown atomic.Bool
	clLn         net.Listener
}

func NewServer(config *ServerConfig) (*Server, error) {
	if config.Logger == nil {
		config.Logger = log.Default()
	}

	if config.ClientListenerAddress == "" {
		config.ClientListenerAddress = ":9000"
	}

	logger := config.Logger
	sshConfig := &ssh.ServerConfig{
		NoClientAuth:            config.NoClientAuth,
		PublicKeyAuthAlgorithms: config.PublicKeyAuthAlgorithms,
		PublicKeyCallback:       config.PublicKeyCallback,
		AuthLogCallback: func(conn ssh.ConnMetadata, method string, err error) {
			if err != nil {
				if errors.Is(err, ssh.ErrNoAuth) {
					logger.Debug("auth log callback but auth has yet to be passed")
					return
				}

				logger.Error("failed auth attempt", "method", method, "raddr", conn.RemoteAddr(), "err", err)
				return
			}

			logger.Info("auth passed", "method", method, "raddr", conn.RemoteAddr())
		},
	}

	sshConfig.AddHostKey(config.Signer)

	return &Server{
		cAddr:    config.ClientListenerAddress,
		logger:   config.Logger,
		sshcfg:   sshConfig,
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

			subdomain := generateSubdomain()

			config := *s.sshcfg
			config.BannerCallback = func(conn ssh.ConnMetadata) string {
				return subdomain
			}
			sshConn, chans, reqs, err := ssh.NewServerConn(netConn, &config)
			if err != nil {
				s.logger.Error("failed to create server conn", "err", err)
				return
			}

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
		Transport: s,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetXForwarded()

			s.logger.Info("Rewriting", "method", r.In.Method, "path", r.In.Host+r.In.URL.String())

			sesh, ok := r.In.Context().Value(SessionContextKey).(*Session)
			if !ok {
				s.logger.Fatal("Rewrite: Invalid session object in context", "proto", r.In.Proto, "method", r.In.Method, "host", r.In.Host, "path", r.In.URL.String())
			}

			url, _ := url.Parse(fmt.Sprintf("http://%s", sesh.subdomain))
			r.SetURL(url)
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

func (s *Server) RoundTrip(r *http.Request) (*http.Response, error) {
	sesh, ok := r.Context().Value(SessionContextKey).(*Session)
	if !ok {
		s.logger.Fatal("Invalid session object in context", "proto", r.Proto, "method", r.Method, "host", r.Host, "path", r.URL.String())
	}

	respch, errch, err := sesh.roundTrip(r)
	if err != nil {
		return nil, err
	}

	ctx := r.Context()
	select {
	case resp := <-respch:
		return resp, nil
	case err := <-errch:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
