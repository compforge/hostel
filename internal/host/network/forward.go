package network

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

// TCPForwarder publishes exactly one internally selected endpoint. It has no
// caller-supplied destination, reconnect or replay semantics.
type TCPForwarder struct {
	allocation *PortAllocation
	cancel     context.CancelFunc
	done       chan struct{}
}

func NewTCPForwarder(ports *PortManager, owner, target string) (*TCPForwarder, error) {
	a, listener, err := ports.Listen(owner, "0.0.0.0:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &TCPForwarder{allocation: a, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(f.done)
		var wg sync.WaitGroup
		defer wg.Wait()
		// Bound connections and idle time independently of service lifetime.
		slots := make(chan struct{}, 128)
		for {
			in, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				in.Close()
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				defer in.Close()
				stop := context.AfterFunc(ctx, func() { in.Close() })
				defer stop()
				out, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", target)
				if err != nil {
					return
				}
				defer out.Close()
				stopOut := context.AfterFunc(ctx, func() { out.Close() })
				defer stopOut()
				// Connections are bounded to the same two-hour operation maximum.
				_ = in.SetDeadline(time.Now().Add(2 * time.Hour))
				_ = out.SetDeadline(time.Now().Add(2 * time.Hour))
				copied := make(chan struct{})
				go func() {
					defer close(copied)
					_, _ = io.Copy(out, in)
					if tcp, ok := out.(*net.TCPConn); ok {
						_ = tcp.CloseWrite()
					}
				}()
				_, _ = io.Copy(in, out)
				in.Close()
				out.Close()
				<-copied
			}()
		}
	}()
	return f, nil
}
func (f *TCPForwarder) Port() int    { return f.allocation.Port() }
func (f *TCPForwarder) Close() error { f.cancel(); err := f.allocation.Release(); <-f.done; return err }
