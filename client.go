package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"

	"github.com/charmbracelet/log"
)

func newClient(ctx context.Context) *http.Server {
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
