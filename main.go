package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/ssh"
)

func runServer(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	// log.Debug("listening for connection on port 9000")
	// ln, err := net.Listen("tcp", "127.0.0.1:9000")
	// if err != nil {
	// 	log.Fatal("failed to listen on port 9000:", err)
	// }
	// conn, err := ln.Accept()
	// if err != nil {
	// 	log.Fatal("failed to accept new network conn", "err", err)
	// }

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

	privateKeyBytes, err := os.ReadFile(os.Getenv("KEYS_DIR") + "\\id_ed25519")
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
		log.Fatal("failed to listen on port 9000:", err)
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

	chConn := ChannelConn{
		Channel: channel,
		laddr:   conn.LocalAddr(),
		raddr:   conn.RemoteAddr(),
	}

	server := newServer(chConn)

	go func() {
		log.Info("Serving")
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()

	ctxShutDown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	log.Info("Shutting down")
	err = server.Shutdown(ctxShutDown)
	if err != nil {
		return fmt.Errorf("err on shutdown, got: %s", err)
	}

	return nil
}

func runClient(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	readych := make(chan *SingleConnListener)
	go func() {
		// conn, err := net.Dial("tcp", "127.0.0.1:9000")
		// if err != nil {
		// 	log.Fatal("client failed to dial", "err", err)
		// }

		conn, err := openSSHConn()
		if err != nil {
			log.Fatal(err)
		}

		ln := &SingleConnListener{
			ch: make(chan net.Conn, 1),
		}
		err = ln.ServeConn(conn)
		if err != nil {
			log.Fatal(err)
		}
		readych <- ln
	}()

	var ln *SingleConnListener
	select {
	case ln = <-readych:
	case <-ctx.Done():
		return ctx.Err()
	}

	server := newClient(ctx)

	go func() {
		log.Info("Serving")
		err := server.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()

	ctxShutDown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	log.Info("Shutting down")
	err := server.Shutdown(ctxShutDown)
	if err != nil {
		return fmt.Errorf("err on shutdown, got: %s", err)
	}

	return nil
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal(err)
	}

	log.SetLevel(log.DebugLevel)

	if len(os.Args) < 2 {
		log.Fatal("pass either \"client\" or \"server\" flag")
	}

	flag := os.Args[1]

	if strings.EqualFold("server", flag) {
		err = runServer(context.Background())
	} else if strings.EqualFold("client", flag) {
		err = runClient(context.Background())
	} else {
		log.Fatal("pass either \"client\" or \"server\" flag")
	}

	if err != nil {
		log.Error("failed to run", "err", err)
	}
}
