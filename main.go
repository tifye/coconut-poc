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
	"time"

	"github.com/charmbracelet/log"
)

func runServer(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	server := newServer()

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
	err := server.Shutdown(ctxShutDown)
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
		conn, err := net.Dial("tcp", "127.0.0.1:9000")
		if err != nil {
			log.Fatal("client failed to dial", "err", err)
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
	if len(os.Args) < 2 {
		log.Fatal("pass either \"client\" or \"server\" flag")
	}

	flag := os.Args[1]

	var err error
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
