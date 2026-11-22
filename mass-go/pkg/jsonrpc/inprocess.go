package jsonrpc

import (
	"io"
	"os"
	"sync"
	"time"
)

// NewInProcessPair connects two validated streams without opening a listener.
// Each direction transfers one complete message at a time. A blocked write
// times out and closes only this pair.
func NewInProcessPair(clientWriteTimeout, serverWriteTimeout time.Duration) (client, server *Stream, err error) {
	life := &pairLifetime{done: make(chan struct{})}
	requests, responses := make(chan []byte), make(chan []byte)
	client, err = newStream(inProcessMessages{readFrom: responses, writeTo: requests, life: life}, RoleClient, clientWriteTimeout)
	if err != nil {
		return nil, nil, err
	}
	server, err = newStream(inProcessMessages{readFrom: requests, writeTo: responses, life: life}, RoleServer, serverWriteTimeout)
	if err != nil {
		return nil, nil, err
	}
	return client, server, nil
}

type pairLifetime struct {
	done chan struct{}
	once sync.Once
}

type inProcessMessages struct {
	readFrom <-chan []byte
	writeTo  chan<- []byte
	life     *pairLifetime
}

func (s inProcessMessages) read() ([]byte, error) {
	select {
	case <-s.life.done:
		return nil, io.EOF
	default:
	}
	select {
	case message := <-s.readFrom:
		return message, nil
	case <-s.life.done:
		return nil, io.EOF
	}
}

func (s inProcessMessages) write(message []byte, timeout time.Duration) error {
	select {
	case <-s.life.done:
		return io.ErrClosedPipe
	default:
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case s.writeTo <- message:
		return nil
	case <-s.life.done:
		return io.ErrClosedPipe
	case <-timer.C:
		return os.ErrDeadlineExceeded
	}
}

func (s inProcessMessages) close(int, string) error {
	s.life.once.Do(func() { close(s.life.done) })
	return nil
}
