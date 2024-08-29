package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	log.Println("Dialing")
	conn, err := net.Dial("tcp", ":9000")
	if err != nil {
		log.Fatalln("failed to dial port 9000:", err)
	}

	ln := &SingleConnListener{
		ch: make(chan net.Conn),
	}

	s := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

			log.Println(r.URL.String())
			buff := bytes.Buffer{}
			_, err := io.Copy(&buff, r.Body)
			if err != nil {
				log.Println("err copying request body:", err)
				return
			}

			w.Header().Add("Context-Type", "text")
			_, err = w.Write([]byte("this is from my local"))
			if err != nil {
				log.Fatal(err)
			}

			log.Println(buff.String())
		}),
	}

	go func() {
		err := s.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			log.Fatalln("server error:", err)
		}
	}()

	err = ln.ServeConn(conn)
	if err != nil {
		log.Fatal(err)
	}

	<-ctx.Done()
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
	log.Println("Accept")
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
	log.Println("Addr")
	return EmptyAddr{}
}

type EmptyAddr struct{}

func (EmptyAddr) Network() string {
	return ""
}
func (EmptyAddr) String() string {
	return ""
}
