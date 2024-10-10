package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/ssh"
)

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

func openSSHConn() (net.Conn, error) {
	publicKeyBytes, err := os.ReadFile(os.Getenv("KEYS_DIR") + "\\id_ed25519.pub")
	if err != nil {
		return nil, fmt.Errorf("failed to read key, got: %s", err)
	}

	publicKey, _, _, _, err := ssh.ParseAuthorizedKey(publicKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse key bytes, got: %s", err)
	}

	config := &ssh.ClientConfig{
		User: "tifye",
		Auth: []ssh.AuthMethod{
			ssh.Password("mino"),
		},
		HostKeyCallback: ssh.FixedHostKey(publicKey),
	}
	client, err := ssh.Dial("tcp", "localhost:9000", config)
	if err != nil {
		return nil, err
	}

	ch, reqs, err := client.OpenChannel("tunnel", nil)
	if err != nil {
		return nil, err
	}

	go ssh.DiscardRequests(reqs)

	conn := NetChannelConn{
		Channel: ch,
		laddr:   client.LocalAddr(),
		raddr:   client.RemoteAddr(),
	}

	return conn, nil
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	conn, err := openSSHConn()
	if err != nil {
		return err
	}

	ln := &SingleConnListener{
		ch: make(chan net.Conn, 1),
	}
	err = ln.ServeConn(conn)
	if err != nil {
		return err
	}

	s := &http.Server{
		Handler: &httputil.ReverseProxy{
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetXForwarded()
				uri, _ := url.Parse("http://127.0.0.1:3000")
				log.Info("rewriting", "method", r.In.Method, "req", r.In.URL)
				r.SetURL(uri)
			},
			ModifyResponse: func(r *http.Response) error {
				log.Info("routing back response", "req", r.Request.URL, "status", r.Status, "content-length", r.ContentLength)
				return nil
			},
		},
		BaseContext: func(l net.Listener) context.Context {
			return ctx
		},
		// Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 	log.Println(r.URL.String())
		// 	buff := bytes.Buffer{}
		// 	_, err := io.Copy(&buff, r.Body)
		// 	if err != nil {
		// 		log.Println("err copying request body:", err)
		// 		return
		// 	}

		// 	log.Println(buff.String())

		// 	w.Header().Add("Content-Type", "text")
		// 	_, err = w.Write([]byte("mino"))
		// 	if err != nil {
		// 		log.Println(err)
		// 	}
		// }),
	}

	go func() {
		err := s.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			log.Fatal("server error:", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()
	err = s.Shutdown(shutdownCtx)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal(err)
	}

	err = run()
	if err != nil {
		log.Fatal(err)
	}
}
