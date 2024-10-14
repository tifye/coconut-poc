package main

import (
	"net"
	"net/http"
	"net/http/httptrace"
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

			trace := &httptrace.ClientTrace{
				ConnectDone: func(network, addr string, err error) {
					log.Debug("Dial complete", "network", "addr", "err", err)
				},
				GetConn: func(hostPort string) {
					log.Debug("GetConn", "hostPort", hostPort)
				},
				GotConn: func(info httptrace.GotConnInfo) {
					log.Debug("GotConn", "reused", info.Reused, "wasIdle", info.WasIdle)
				},
			}
			r.Out = r.Out.WithContext(httptrace.WithClientTrace(r.Out.Context(), trace))
		},
		ErrorLog: log.StandardLog(),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Error("http: proxy error", "req", r.URL.String(), "err", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}

	ready := make(chan struct{}, 1)
	ready <- struct{}{}

	mux := http.ServeMux{}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		<-ready
		proxyHandler.ServeHTTP(w, r)
		ready <- struct{}{}
	})

	server := &http.Server{
		Addr:         "127.0.0.1:9997",
		Handler:      &mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		ConnState: func(conn net.Conn, state http.ConnState) {
			log.Debug("Conn state changed", "state", state.String())
		},
	}

	return server
}
