package pkg

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/charmbracelet/log"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
)

type tunnel struct {
	reqs    <-chan *ssh.Request
	Conn    ChannelConn
	sshChan ssh.Channel
	onclose func(*tunnel)
}

func (t *tunnel) Close() error {
	err := t.sshChan.Close()
	t.onclose(t)
	return err
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

	chConn := ChannelConn{
		Channel: channel,
		laddr:   sshConn.LocalAddr(),
		raddr:   sshConn.RemoteAddr(),
	}

	tunnels := make([]*tunnel, 1)
	tunnelCh := make(chan *tunnel, 1)
	tunnels[0] = &tunnel{
		reqs:    requests,
		Conn:    chConn,
		sshChan: channel,
		onclose: func(t *tunnel) {
			go func() {
				tunnelCh <- t
			}()
		},
	}
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
		eg.Go(t.sshChan.Close)
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
