package pkg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
)

type tunnel struct {
	laddr   net.Addr
	raddr   net.Addr
	sshChan ssh.Channel
	onclose func(*tunnel)
}

func (t *tunnel) LocalAddr() net.Addr {
	return t.laddr
}

func (t *tunnel) RemoteAddr() net.Addr {
	return t.raddr
}

func (t *tunnel) SetDeadline(_ time.Time) error {
	log.Info("SetDeadline called")
	return nil
}

func (t *tunnel) SetReadDeadline(_ time.Time) error {
	log.Info("SetReadDeadline called")
	return nil
}

func (t *tunnel) SetWriteDeadline(_ time.Time) error {
	log.Info("SetWriteDeadline called")
	return nil
}

func (t *tunnel) Read(buf []byte) (n int, err error) {
	return t.sshChan.Read(buf)
}

func (t *tunnel) Write(buf []byte) (n int, err error) {
	return t.sshChan.Write(buf)
}

func newTunnel(sshChan ssh.Channel, raddr, laddr net.Addr, onclose func(*tunnel)) *tunnel {
	if onclose == nil {
		onclose = func(*tunnel) {}
	}
	return &tunnel{
		sshChan: sshChan,
		onclose: onclose,
		laddr:   laddr,
		raddr:   raddr,
	}
}

func (t *tunnel) Close() error {
	t.onclose(t)
	return nil
}

func (t *tunnel) Cleanup() error {
	return t.sshChan.Close()
}

type Session struct {
	subdomain string
	logger    *log.Logger
	sshConn   *ssh.ServerConn
	chans     <-chan ssh.NewChannel
	reqs      <-chan *ssh.Request
	tunnels   []*tunnel
	tunnelCh  chan *tunnel
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

	tunnels := make([]*tunnel, 1)
	tunnelCh := make(chan *tunnel, 1)
	tunnels[0] = newTunnel(channel, sshConn.RemoteAddr(), sshConn.LocalAddr(), func(t *tunnel) {
		logger := logger.WithPrefix("tunnel")
		go func() {
			logger.Debug("returning tunnel to pool")
			tunnelCh <- t
		}()
	})
	tunnelCh <- tunnels[0]

	return &Session{
		subdomain: subdomain,
		sshConn:   sshConn,
		chans:     chans,
		reqs:      reqs,
		tunnels:   tunnels,
		tunnelCh:  tunnelCh,
		logger:    logger,
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

func (s *Session) Accept(ctx context.Context) (*tunnel, error) {
	select {
	case t, ok := <-s.tunnelCh:
		if !ok {
			return nil, errors.New("tunnel channel closed")
		}
		return t, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
