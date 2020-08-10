package pool

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type ConnProvider struct {
	mu          sync.RWMutex
	idleConnMap map[string]chan net.Conn
	options     *ConnOptions
	closed      int32
}

func NewConnProvider(opt *ConnOptions) *ConnProvider {
	return &ConnProvider{
		options: opt,
		mu:      sync.RWMutex{},
		idleConnMap: make(map[string] chan net.Conn),
	}
}

// Get returned a idle net.Conn
func (p *ConnProvider) Get(addr string) (net.Conn, error) {
	closed := atomic.LoadInt32(&p.closed)
	if closed == 1 {
		return nil, errors.New("pool is closed")
	}

	p.mu.Lock()
	if _, ok := p.idleConnMap[addr]; !ok {
		p.mu.Unlock()
		return nil, errors.New("no idle conn")
	}
	p.mu.Unlock()

RETRY:
	select {
	case conn := <-p.idleConnMap[addr]:
		_, err := conn.Read([]byte{})
		if err != nil || err == io.EOF {
			// conn is close Or timeout
			_ = conn.Close()
			goto RETRY
		}
		return conn, nil
	default:
		return nil, errors.New("no idle conn")
	}
}

// Put place a idle net.Conn into the pool
func (p *ConnProvider) Put(conn net.Conn) error {
	closed := atomic.LoadInt32(&p.closed)
	if closed == 1 {
		return errors.New("pool is closed")
	}

	addr := conn.RemoteAddr().String()

	p.mu.Lock()
	if _, ok := p.idleConnMap[addr]; !ok {
		p.idleConnMap[addr] = make(chan net.Conn, p.options.IdleMaxCap)
	}
	p.mu.Unlock()

	// set conn timeout
	err := conn.SetDeadline(time.Now().Add(p.options.Timeout))
	if err != nil {
		_ = conn.Close()
		return err
	}

	// set idle conn
	select {
	case p.idleConnMap[addr] <- conn:
		return nil
	default:
		err := conn.Close()
		return fmt.Errorf("beyond max capacity. conn closed: %v", err)
	}
}

func (p *ConnProvider) Release() error {
	closed := atomic.LoadInt32(&p.closed)
	if closed == 1 {
		return errors.New("pool is closed")
	}

	atomic.StoreInt32(&p.closed, 1)

	for _, connChan := range p.idleConnMap {
		close(connChan)
		for conn, ok := <-connChan; ok; {
			_ = conn.Close()
		}
	}
	return nil
}
