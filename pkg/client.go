package pkg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"
	"golang.org/x/crypto/ssh"
)

type Client struct {
	logger      *log.Logger
	sshCfn      *ssh.ClientConfig
	proxyToAddr string
}

func NewClient(proxyToAddr string, logger *log.Logger) (*Client, error) {
	// Todo: how to properly read/create a key?
	publicKeyBytes, err := os.ReadFile(os.Getenv("KEYS_DIR") + "\\id_ed25519.pub")
	if err != nil {
		return nil, fmt.Errorf("failed to read key, got: %s", err)
	}

	publicKey, _, _, _, err := ssh.ParseAuthorizedKey(publicKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse key bytes, got: %s", err)
	}

	// Todo: sensible defaults
	config := &ssh.ClientConfig{
		User: "tifye",
		Auth: []ssh.AuthMethod{
			ssh.Password("mino"),
		},
		HostKeyCallback: ssh.FixedHostKey(publicKey),
	}

	return &Client{
		logger:      logger,
		sshCfn:      config,
		proxyToAddr: proxyToAddr,
	}, nil
}

func (c *Client) Start(ctx context.Context, serverAddr string) error {
	errCh := make(chan error)
	connCh := make(chan net.Conn)
	go func() {
		client, err := ssh.Dial("tcp", serverAddr, c.sshCfn)
		if err != nil {
			errCh <- err
			return
		}

		ch, reqs, err := client.OpenChannel("tunnel", nil)
		if err != nil {
			errCh <- err
			return
		}

		go ssh.DiscardRequests(reqs)

		conn := NetChannelConn{
			Channel: ch,
			laddr:   client.LocalAddr(),
			raddr:   client.RemoteAddr(),
		}
		connCh <- conn
	}()

	var conn net.Conn
	select {
	case err := <-errCh:
		return err
	case conn = <-connCh:
	case <-ctx.Done():
		return ctx.Err()
	}

	var ln *SingleConnListener = &SingleConnListener{
		ch: make(chan net.Conn, 1),
	}

	err := ln.ServeConn(conn)
	if err != nil {
		return err
	}

	proxy := c.newClientProxy(ctx)
	go func() {
		c.logger.Info("Serving")
		err := proxy.Serve(ln)
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			errCh <- nil
		} else {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	shutDownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.logger.Info("shutting down proxy")
	err = proxy.Shutdown(shutDownCtx)
	if err != nil {
		return fmt.Errorf("proxy shutdown: %s", err)
	}

	return nil
}

type NetChannelConn struct {
	ssh.Channel
	laddr net.Addr
	raddr net.Addr
}

func (cc NetChannelConn) LocalAddr() net.Addr {
	return cc.laddr
}
func (cc NetChannelConn) RemoteAddr() net.Addr {
	return cc.raddr
}
func (cc NetChannelConn) SetDeadline(t time.Time) error {
	return nil
}
func (cc NetChannelConn) SetReadDeadline(t time.Time) error {
	return nil
}
func (cc NetChannelConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func (c *Client) newClientProxy(ctx context.Context) *http.Server {
	s := &http.Server{
		Handler: &httputil.ReverseProxy{
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetXForwarded()
				uri, _ := url.Parse(c.proxyToAddr)
				c.logger.Info("rewriting", "method", r.In.Method, "req", r.In.URL)
				r.SetURL(uri)
			},
			ModifyResponse: func(r *http.Response) error {
				c.logger.Info("routing back response", "req", r.Request.URL, "status", r.Status, "content-length", r.ContentLength)
				return nil
			},
		},
		BaseContext: func(l net.Listener) context.Context {
			return ctx
		},
	}

	return s
}

type SingleConnListener struct {
	ch     chan net.Conn
	closed atomic.Bool
}

func (l *SingleConnListener) ServeConn(conn net.Conn) error {
	if l.closed.Load() {
		return net.ErrClosed
	}

	l.ch <- conn
	return nil
}

func (l *SingleConnListener) Accept() (net.Conn, error) {
	conn, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return conn, nil
}

func (l *SingleConnListener) Close() error {
	log.Debug("closing SingleConnListener")
	if l.closed.Load() {
		return net.ErrClosed
	}

	close(l.ch)
	return nil
}

func (l *SingleConnListener) Addr() net.Addr {
	return EmptyAddr{}
}

type EmptyAddr struct{}

func (EmptyAddr) Network() string {
	return ""
}
func (EmptyAddr) String() string {
	return ""
}
