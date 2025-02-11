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
	"github.com/tifye/tunnel/assert"
	"golang.org/x/crypto/ssh"
)

type ServerConfig struct {
	Signer                  ssh.Signer
	PublicKeyAuthAlgorithms []string
	PublicKeyCallback       func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error)
	ClientListenerAddress   string
	FrontendAddress         string
	NoClientAuth            bool
	Logger                  *log.Logger
}

type Server struct {
	logger       *log.Logger
	cAddr        string
	fAddr        string
	sshcfg       *ssh.ServerConfig
	sessions     map[string]*Session
	mu           sync.Mutex
	shuttingDown atomic.Bool
	clLn         net.Listener
	proxyLn      net.Listener
	proxy        *http.Server
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
		fAddr:    config.FrontendAddress,
		sessions: make(map[string]*Session, 0),
	}, nil
}

func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cAddr)
	if err != nil {
		log.Fatal("failed to listen on port 9000:", err)
	}
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
	s.proxy = server
	serverLn, err := net.Listen("tcp", s.fAddr)
	if err != nil {
		return fmt.Errorf("failed to start server listener: %s", err)
	}
	s.proxyLn = serverLn

	go func() {
		s.logger.Info("Serving")
		err := server.Serve(serverLn)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("server failed", "err", err)
			s.Close(context.Background())
		}
	}()
	return nil
}

func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.shuttingDown.Load() {
		return errors.New("already closed")
	}
	s.shuttingDown.Store(true)

	for _, sesh := range s.sessions {
		sesh.Close(ctx)
	}
	s.sessions = nil

	err := s.proxy.Shutdown(ctx)
	if err != nil {
		return err
	}

	err = s.clLn.Close()
	if err != nil {
		s.logger.Error("client listener close", "err", err)
	}

	err = s.proxyLn.Close()
	if err != nil {
		s.logger.Error("proxy listener close", "err", err)
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
			sesh, ok := r.In.Context().Value(SessionContextKey).(*Session)
			assert.Assert(ok, "expected session object")

			sesh.Logger().Info("rewriting", "method", r.In.Method, "path", r.In.Host+r.In.URL.String())

			r.SetXForwarded()
			url, _ := url.Parse(fmt.Sprintf("http://%s", sesh.sshConn.RemoteAddr().String()))
			r.SetURL(url)
		},
		ErrorLog: s.logger.StandardLog(),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			sesh, ok := r.Context().Value(SessionContextKey).(*Session)
			assert.Assert(ok, "expected session object")
			sesh.Logger().Error("http: proxy error", "path", r.URL.Path, "err", err)
			w.WriteHeader(http.StatusBadGateway)
		},
		ModifyResponse: func(r *http.Response) error {
			sesh, ok := r.Request.Context().Value(SessionContextKey).(*Session)
			assert.Assert(ok, "expected session object")
			sesh.Logger().Info("routing back response", "req", r.Request.URL, "status", r.Status, "content-length", r.ContentLength)
			return nil
		},
	}

	mux := http.ServeMux{}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
			s.logger.Warn("could not find session for incoming request", "host", r.Host, "subpart", subpart)
			w.WriteHeader(http.StatusNotFound)
			w.Write(nil)
			return
		}

		sesh.Logger().Info("serving request", "proto", r.Proto, "path", r.URL.Path)

		ctx := context.WithValue(r.Context(), SessionContextKey, sesh)
		r = r.WithContext(ctx)
		proxyHandler.ServeHTTP(w, r)
	})

	server := &http.Server{
		Addr:         s.fAddr,
		Handler:      &mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return server
}

func (s *Server) RoundTrip(r *http.Request) (*http.Response, error) {
	sesh, ok := r.Context().Value(SessionContextKey).(*Session)
	assert.Assert(ok, "expected session object")

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
