package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"time"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	cchan := make(chan net.Conn)

	log.Println("Listening for conns on port 9000")
	ln, err := net.Listen("tcp", ":9000")
	if err != nil {
		log.Fatalln("failed to listen on port 9000:", err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatalln("err on accept:", err)
		}
		cchan <- conn
	}()

	conn := <-cchan
	s := &http.Server{
		Addr: ":9997",
		Handler: &httputil.ReverseProxy{
			Transport: &http.Transport{
				Dial: func(_, addr string) (net.Conn, error) {
					log.Println(addr)
					return conn, nil
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
		err := s.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalln(err)
		}
	}()

	<-ctx.Done()
	ctxShutDown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	log.Println("Shutting down")
	err = s.Shutdown(ctxShutDown)
	if err != nil {
		log.Fatal(err)
	}
}
