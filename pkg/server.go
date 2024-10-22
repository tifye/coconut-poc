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
	"time"

	"github.com/charmbracelet/log"
	"golang.org/x/crypto/ssh"
)

type Server struct {
	logger   *log.Logger
	cAddr    string
	sshCfg   *ssh.ServerConfig
	sessions map[string]*Session
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

	netConn, err := ln.Accept()
	if err != nil {
		return fmt.Errorf("failed to accept new network conn, got: %s", err)
	}

	sshConn, chans, reqs, err := ssh.NewServerConn(netConn, s.sshCfg)
	if err != nil {
		return fmt.Errorf("failed to create server conn, got: %s", err)
	}

	sesh, err := newSession(ctx, s.logger, "meep", sshConn, chans, reqs)
	if err != nil {
		return fmt.Errorf("failed to create new session: %s", err)
	}

	s.sessions[sesh.subdomain] = sesh

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

func (s *Server) newServerProxy() *http.Server {
	proxyHandler := &httputil.ReverseProxy{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				s.logger.Info("Dial called", "addr", addr)

				sesh, ok := ctx.Value("session").(*Session)
				if !ok {
					s.logger.Fatal("DialContext: Invalid session object in context", "network", network, "addr", addr)
				}

				s.logger.Print(sesh.subdomain)

				return sesh.mainTunnel.Conn, nil
			},
			Proxy: http.ProxyFromEnvironment,
		},
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetXForwarded()

			s.logger.Info("Rewriting", "method", r.In.Method, "path", r.In.Host+r.In.URL.String())

			url, _ := url.Parse("http://meep")
			r.SetURL(url)

			sesh, ok := r.In.Context().Value("session").(*Session)
			if !ok {
				s.logger.Fatal("Rewrite: Invalid session object in context", "proto", r.In.Proto, "method", r.In.Method, "host", r.In.Host, "path", r.In.URL.String())
			}

			s.logger.Print(sesh.subdomain)

			trace := &httptrace.ClientTrace{
				ConnectDone: func(network, addr string, err error) {
					s.logger.Debug("Dial complete", "network", "addr", "err", err)
				},
				GetConn: func(hostPort string) {
					s.logger.Debug("GetConn", "hostPort", hostPort)
				},
				GotConn: func(info httptrace.GotConnInfo) {
					s.logger.Debug("GotConn", "reused", info.Reused, "wasIdle", info.WasIdle)
				},
			}
			r.Out = r.Out.WithContext(httptrace.WithClientTrace(r.Out.Context(), trace))
		},
		ErrorLog: s.logger.StandardLog(),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.logger.Error("http: proxy error", "path", r.URL.Path, "err", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}

	ready := make(chan struct{}, 1)
	ready <- struct{}{}

	mux := http.ServeMux{}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		s.logger.Debug("Proto", r.Proto)

		s.logger.Debug("Waiting to serve request", "path", r.URL.String())

		<-ready
		defer func() {
			ready <- struct{}{}
			s.logger.Debug("Finished serving request", "path", r.URL.Path)
		}()

		s.logger.Debug("Serving request", "path", r.URL.String())

		subpart, _, _ := strings.Cut(r.Host, ".")
		sesh, found := s.sessions[subpart]
		if !found {
			s.logger.Warn("Could not find session for incoming request", "host", r.Host, "subpart", subpart)
			w.WriteHeader(http.StatusNotFound)
			w.Write(nil)
			return
		}

		ctx := context.WithValue(r.Context(), "session", sesh)
		r = r.WithContext(ctx)

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

type ChannelConn struct {
	ssh.Channel
	laddr net.Addr
	raddr net.Addr
}

func (cc ChannelConn) LocalAddr() net.Addr {
	return cc.laddr
}

func (cc ChannelConn) RemoteAddr() net.Addr {
	return cc.raddr
}

func (cc ChannelConn) SetDeadline(t time.Time) error {
	log.Info("SetDeadline called")
	return nil
}

func (cc ChannelConn) SetReadDeadline(t time.Time) error {
	log.Info("SetReadDeadline called")
	return nil
}

func (cc ChannelConn) SetWriteDeadline(t time.Time) error {
	log.Info("SetWriteDeadline called")
	return nil
}

func (cc ChannelConn) Close() error {
	log.Info("Close Channel called")
	return nil
}

type SessionTunnelTransport struct {
}
