package network

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestDNSForwarderPreservesTCPAndUDPWireMessages(t *testing.T) {
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udpAddr, err := net.ResolveUDPAddr("udp4", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go func() {
		buf := make([]byte, 4096)
		n, peer, e := udp.ReadFromUDP(buf)
		if e == nil {
			_, _ = udp.WriteToUDP(buf[:n], peer)
		}
	}()
	go func() {
		c, e := tcp.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		var size [2]byte
		if _, e = io.ReadFull(c, size[:]); e != nil {
			return
		}
		data := make([]byte, binary.BigEndian.Uint16(size[:]))
		if _, e = io.ReadFull(c, data); e == nil {
			_, _ = c.Write(append(size[:], data...))
		}
	}()
	d := newDNSForwarder("127.0.0.1", []string{tcp.Addr().String()})
	d.address = "127.0.0.1:0"
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, tc := range []struct{ protocol, address string }{{"udp", d.udp.LocalAddr().String()}, {"tcp", d.tcp.Addr().String()}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		payload := []byte{12, 34, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		got, err := dnsExchange(ctx, tc.protocol, tc.address, payload)
		cancel()
		if err != nil || string(got) != string(payload) {
			t.Fatalf("%s got %v: %v", tc.protocol, got, err)
		}
	}
}
