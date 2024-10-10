package main

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/charmbracelet/log"
)

func newServer() *http.Server {
	log.Debug("listening for connection on port 9000")
	ln, err := net.Listen("tcp", "127.0.0.1:9000")
	if err != nil {
		log.Fatal("failed to listen on port 9000:", err)
	}
	nConn, err := ln.Accept()
	if err != nil {
		log.Fatal("failed to accept new network conn", "err", err)
	}

	proxyHandler := &httputil.ReverseProxy{
		Transport: &http.Transport{
			Dial: func(_, addr string) (net.Conn, error) {
				log.Info("dial called", "addr", addr)
				return nConn, nil
			},
			Proxy: http.ProxyFromEnvironment,
		},
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetXForwarded()
			log.Info("rewrite", "method", r.In.Method, "path", r.In.Host+r.In.URL.String())
			url, _ := url.Parse("http://localhost:3000")
			r.SetURL(url)
		},
		ErrorLog: log.StandardLog(),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Error("http: proxy error", "req", r.URL.String(), "err", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}

	mux := http.ServeMux{}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		proxyHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /meep", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("meep"))
	})

	server := &http.Server{
		Addr:         "127.0.0.1:9997",
		Handler:      &mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return server
}
