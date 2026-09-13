package network

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestPublishedEndpointForwardsAndClosesExistingConnections(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	ports, _ := NewPortManager(24200, 24300)
	defer ports.Close()
	f, err := NewTCPForwarder(ports, "bed/service/publish", backend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", f.allocation.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = conn.Write([]byte("stream"))
	data := make([]byte, 6)
	if _, err := io.ReadFull(conn, data); err != nil || string(data) != "stream" {
		t.Fatalf("forwarded %q: %v", data, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(data); err == nil {
		t.Fatal("old published connection survived Close")
	}
	if len(ports.Status()) != 0 {
		t.Fatal("publish allocation retained")
	}
}
