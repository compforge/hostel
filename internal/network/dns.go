package network

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// dnsForwarder preserves the carrier's DNS (including Docker's loopback DNS)
// without exposing its network namespace to Bed processes. All requests are
// bounded by a common concurrency limit and timeout; this is not a DNS cache.
type dnsForwarder struct {
	policy    *policyControl
	address   string
	upstreams []string
	udp       *net.UDPConn
	tcp       net.Listener
	slots     chan struct{}
	stop      context.CancelFunc
	wg        sync.WaitGroup
}

func newDNSForwarder(address string, upstreams []string) *dnsForwarder {
	return &dnsForwarder{address: net.JoinHostPort(address, "53"), upstreams: upstreams, slots: make(chan struct{}, 64)}
}
func (d *dnsForwarder) Start() error {
	addr, err := net.ResolveUDPAddr("udp4", d.address)
	if err != nil {
		return err
	}
	d.udp, err = net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("network: DNS UDP: %w", err)
	}
	d.tcp, err = net.Listen("tcp4", d.address)
	if err != nil {
		_ = d.udp.Close()
		return fmt.Errorf("network: DNS TCP: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.stop = cancel
	d.wg.Add(2)
	go func() { defer d.wg.Done(); d.serveUDP(ctx) }()
	go func() { defer d.wg.Done(); d.serveTCP(ctx) }()
	return nil
}
func (d *dnsForwarder) Close() {
	if d.stop != nil {
		d.stop()
	}
	if d.udp != nil {
		_ = d.udp.Close()
	}
	if d.tcp != nil {
		_ = d.tcp.Close()
	}
	d.wg.Wait()
}
func (d *dnsForwarder) serveUDP(ctx context.Context) {
	for {
		buf := make([]byte, 65535)
		n, peer, err := d.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		select {
		case d.slots <- struct{}{}:
		default:
			continue
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer func() { <-d.slots }()
			if response, err := d.exchange(ctx, "udp", buf[:n]); err == nil {
				_, _ = d.udp.WriteToUDP(response, peer)
			}
		}()
	}
}
func (d *dnsForwarder) serveTCP(ctx context.Context) {
	for {
		conn, err := d.tcp.Accept()
		if err != nil {
			return
		}
		select {
		case d.slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer func() { <-d.slots }()
			defer conn.Close()
			// One query per connection; clients may reconnect. No idle connection can
			// retain an admission slot indefinitely.
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			var size [2]byte
			if _, err := io.ReadFull(conn, size[:]); err != nil {
				return
			}
			payload := make([]byte, binary.BigEndian.Uint16(size[:]))
			if _, err := io.ReadFull(conn, payload); err != nil {
				return
			}
			response, err := d.exchange(ctx, "tcp", payload)
			if err != nil {
				return
			}
			binary.BigEndian.PutUint16(size[:], uint16(len(response)))
			_, _ = conn.Write(append(size[:], response...))
		}()
	}
}
func (d *dnsForwarder) exchange(ctx context.Context, protocol string, payload []byte) ([]byte, error) {
	if d.policy != nil {
		return d.policy.exchange(ctx, payload, func() ([]byte, error) { return d.exchangeUpstream(ctx, protocol, payload) })
	}
	return d.exchangeUpstream(ctx, protocol, payload)
}

func (d *dnsForwarder) exchangeUpstream(ctx context.Context, protocol string, payload []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var last error
	for _, address := range d.upstreams {
		result, err := dnsExchange(ctx, protocol, address, payload)
		if err == nil {
			return result, nil
		}
		last = err
	}
	return nil, last
}
func dnsExchange(ctx context.Context, protocol, address string, payload []byte) ([]byte, error) {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, protocol, address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	if protocol == "udp" {
		if _, err = conn.Write(payload); err != nil {
			return nil, err
		}
		response := make([]byte, 65535)
		n, err := conn.Read(response)
		return response[:n], err
	}
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(len(payload)))
	if _, err = conn.Write(append(size[:], payload...)); err != nil {
		return nil, err
	}
	if _, err = io.ReadFull(conn, size[:]); err != nil {
		return nil, err
	}
	response := make([]byte, binary.BigEndian.Uint16(size[:]))
	_, err = io.ReadFull(conn, response)
	return response, err
}
