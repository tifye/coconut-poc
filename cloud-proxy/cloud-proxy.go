package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/crypto/ssh"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatalln(err)
	}

	err = sshServer()
	if err != nil {
		log.Fatalln(err)
	}
}

func sshServer() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			fmt.Println(c.User(), string(pass))
			return nil, nil
		},
		BannerCallback: func(conn ssh.ConnMetadata) string {
			return "MINO"
		},
		NoClientAuth: true,
	}

	privateKeyBytes, err := os.ReadFile(os.Getenv("KEYS_DIR") + "\\id_rsa")
	if err != nil {
		return fmt.Errorf("failed to read private key, got: %s", err)
	}
	privateKey, err := ssh.ParsePrivateKey(privateKeyBytes)
	if err != nil {
		return fmt.Errorf("failed to parse private key bytes, got: %s", err)
	}
	config.AddHostKey(privateKey)

	ln, err := net.Listen("tcp", "0.0.0.0:9000")
	if err != nil {
		log.Fatalln("failed to listen on port 9000:", err)
	}
	nConn, err := ln.Accept()
	if err != nil {
		return fmt.Errorf("failed to accept new network conn, got: %s", err)
	}

	conn, chans, reqs, err := ssh.NewServerConn(nConn, config)
	if err != nil {
		return fmt.Errorf("failed to create server conn, got: %s", err)
	}

	wg := sync.WaitGroup{}
	defer wg.Wait()

	wg.Add(1)
	go func() {
		ssh.DiscardRequests(reqs)
		wg.Done()
	}()

	newChannelReq := <-chans
	if newChannelReq.ChannelType() != "tunnel" {
		err := newChannelReq.Reject(ssh.UnknownChannelType, "Only accepts tunnel channels")
		if err != nil {
			return fmt.Errorf("err when rejecting channel, got: %s", err)
		}
	}

	channel, requests, err := newChannelReq.Accept()
	if err != nil {
		return fmt.Errorf("err when accepting, got: %s", err)
	}

	wg.Add(1)
	go func(in <-chan *ssh.Request) {
		ssh.DiscardRequests(in)
		wg.Done()
	}(requests)

	chanConn := ChannelConn{
		Channel: channel,
		laddr:   conn.LocalAddr(),
		raddr:   conn.RemoteAddr(),
	}

	server := &http.Server{
		Addr: "0.0.0.0:9997",
		Handler: &httputil.ReverseProxy{
			Transport: &http.Transport{
				Dial: func(_, addr string) (net.Conn, error) {
					log.Println(addr)
					return chanConn, nil
				},
			},
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetXForwarded()
				log.Println(r.In.URL.String())
				url, _ := url.Parse("http://localhost:8080/")
				r.SetURL(url)
			},
		},
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Println("serving")
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalln(err)
		}
	}()

	<-ctx.Done()
	ctxShutDown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	log.Println("Shutting down")
	err = server.Shutdown(ctxShutDown)
	if err != nil {
		return fmt.Errorf("err on shutdown, got: %s", err)
	}

	return nil
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
