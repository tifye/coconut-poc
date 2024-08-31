package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/crypto/ssh"
)

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
	log.Println("SetDeadline called")
	return nil
}

func (cc ChannelConn) SetReadDeadline(t time.Time) error {
	log.Println("SetReadDeadline called")
	return nil
}

func (cc ChannelConn) SetWriteDeadline(t time.Time) error {
	log.Println("SetWriteDeadline called")
	return nil
}

func sshClient() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	publicKeyBytes, err := os.ReadFile(os.Getenv("KEYS_DIR") + "\\id_rsa.pub")
	if err != nil {
		return fmt.Errorf("failed to read private key, got: %s", err)
	}
	publicKey, _, _, _, err := ssh.ParseAuthorizedKey(publicKeyBytes)
	if err != nil {
		return fmt.Errorf("failed to parse public key bytes, got: %s", err)
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
		return err
	}
	defer client.Close()

	c, reqs, err := client.OpenChannel("tunnel", nil)
	if err != nil {
		return nil
	}

	wg := sync.WaitGroup{}
	defer wg.Wait()

	wg.Add(1)
	go func(in <-chan *ssh.Request) {
		ssh.DiscardRequests(in)
		wg.Done()
	}(reqs)

	chanConn := ChannelConn{
		Channel: c,
		laddr:   client.LocalAddr(),
		raddr:   client.RemoteAddr(),
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

	err = ln.ServeConn(chanConn)
	if err != nil {
		log.Fatal(err)
	}

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
		log.Fatalln(err)
	}

	err = sshClient()
	if err != nil {
		log.Fatalln(err)
	}
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
