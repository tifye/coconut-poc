package pkg

import (
	"context"
	"fmt"
	"sync"

	"github.com/charmbracelet/log"
	"golang.org/x/crypto/ssh"
)

type SessionChannel struct {
	reqs    <-chan *ssh.Request
	Conn    ChannelConn
	sshChan ssh.Channel
}

type Session struct {
	logger     *log.Logger
	sshConn    *ssh.ServerConn
	chans      <-chan ssh.NewChannel
	reqs       <-chan *ssh.Request
	MainTunnel SessionChannel

	mu sync.Mutex
}

func newSession(ctx context.Context, logger *log.Logger, sshConn *ssh.ServerConn, chans <-chan ssh.NewChannel, reqs <-chan *ssh.Request) (*Session, error) {
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

	return &Session{
		sshConn: sshConn,
		chans:   chans,
		reqs:    reqs,
		MainTunnel: SessionChannel{
			reqs:    requests,
			Conn:    chConn,
			sshChan: channel,
		},
		logger: logger,
		mu:     sync.Mutex{},
	}, nil
}

func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.MainTunnel.sshChan.Close()
}
