package pkg

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/charmbracelet/log"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
)

type tunnel struct {
	logger  *log.Logger
	laddr   net.Addr
	raddr   net.Addr
	sshChan ssh.Channel
}

func newTunnel(logger *log.Logger, sshChan ssh.Channel, raddr, laddr net.Addr) *tunnel {
	return &tunnel{
		logger:  logger,
		sshChan: sshChan,
		laddr:   laddr,
		raddr:   raddr,
	}
}

func (t *tunnel) Cleanup() error {
	t.logger.Debug("Cleanup")
	return t.sshChan.Close()
}

type signalClose struct {
	io.ReadCloser
	done chan<- struct{}
}

func (sc *signalClose) Close() error {
	defer func() {
		sc.done <- struct{}{}
	}()
	return sc.ReadCloser.Close()
}

func (t *tunnel) listen(trch <-chan *tunnelRequest) {
	for tr := range trch {
		res, err := t.roundTrip(tr)
		if err != nil {
			tr.errch <- err
			continue
		}

		ctx := tr.r.Context()
		select {
		case tr.respch <- res:
		case <-ctx.Done():
			tr.errch <- ctx.Err()
		}

		select {
		case <-tr.done:
		case <-ctx.Done():
			tr.errch <- ctx.Err()
		}
	}
}

func (t *tunnel) roundTrip(tr *tunnelRequest) (*http.Response, error) {
	err := tr.r.Write(t.sshChan)
	if err != nil {
		return nil, err
	}

	respReader := bufio.NewReader(t.sshChan)
	resp, err := http.ReadResponse(respReader, tr.r)
	if err != nil {
		return nil, err
	}

	sc := &signalClose{done: tr.done, ReadCloser: resp.Body}
	resp.Body = sc

	return resp, nil
}

type Session struct {
	subdomain string
	logger    *log.Logger
	sshConn   *ssh.ServerConn
	chans     <-chan ssh.NewChannel
	reqs      <-chan *ssh.Request
	tunnels   []*tunnel
	reqch     chan *tunnelRequest
	mu        sync.Mutex
}

func newSession(ctx context.Context, logger *log.Logger, subdomain string, sshConn *ssh.ServerConn, chans <-chan ssh.NewChannel, reqs <-chan *ssh.Request) (*Session, error) {
	go func() {
		ssh.DiscardRequests(reqs)
	}()

	var newChanReq ssh.NewChannel
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case newChanReq = <-chans:
	}

	if newChanReq.ChannelType() != "tunnel" {
		logger.Warn("received non 'tunnel`channel request", "raddr", sshConn.RemoteAddr())

		err := newChanReq.Reject(ssh.UnknownChannelType, "Only accepts tunnel type channels")
		if err != nil {
			return nil, fmt.Errorf("failed on channel reject: %s", err)
		}
	}

	channel, requests, err := newChanReq.Accept()
	if err != nil {
		return nil, fmt.Errorf("failed on channel accept: %s", err)
	}

	logger.Info("accepted channel request", "raddr", sshConn.RemoteAddr(), "channel type", newChanReq.ChannelType())

	go func() {
		ssh.DiscardRequests(requests)
	}()

	trch := make(chan *tunnelRequest)
	tunnels := make([]*tunnel, 1)
	tlogger := logger.WithPrefix("tunnel 1")
	tunnels[0] = newTunnel(tlogger, channel, sshConn.RemoteAddr(), sshConn.LocalAddr())
	go tunnels[0].listen(trch)

	return &Session{
		subdomain: subdomain,
		sshConn:   sshConn,
		chans:     chans,
		reqs:      reqs,
		tunnels:   tunnels,
		logger:    logger,
		reqch:     trch,
		mu:        sync.Mutex{},
	}, nil
}

func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	eg, _ := errgroup.WithContext(ctx)
	for _, t := range s.tunnels {
		eg.Go(t.Cleanup)
	}

	return eg.Wait()
}

type tunnelRequest struct {
	r      *http.Request
	respch chan *http.Response
	errch  chan error
	// Used to signal the closure of the response body.
	done chan struct{}
}

func (s *Session) roundTrip(r *http.Request) (<-chan *http.Response, <-chan error, error) {
	tr := &tunnelRequest{
		r:      r,
		respch: make(chan *http.Response),
		errch:  make(chan error),
		done:   make(chan struct{}),
	}

	ctx := r.Context()
	select {
	case s.reqch <- tr:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}

	return tr.respch, tr.errch, nil
}
