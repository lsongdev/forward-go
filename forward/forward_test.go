package forward

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// echoTCP serves TCP connections by echoing until the client half-closes,
// then writing "bye".
func echoTCP(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
				c.Write([]byte("bye"))
			}()
		}
	}()
	return ln.Addr().String()
}

func freeAddr(t *testing.T, network string) string {
	t.Helper()
	if network == "udp" {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().String()
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func TestNormalize(t *testing.T) {
	r := Rule{Name: " web ", Listen: "8080", Target: "10.0.0.2:80"}
	if err := r.Normalize(); err != nil || r.Name != "web" || r.Protocol != "tcp" || r.Listen != ":8080" {
		t.Fatalf("%+v %v", r, err)
	}
	for _, bad := range []Rule{
		{Listen: ":1", Target: "a:1"},
		{Name: "x", Protocol: "sctp", Listen: ":1", Target: "a:1"},
		{Name: "x", Listen: ":0", Target: "a:1"},
		{Name: "x", Listen: ":1", Target: ":1"},
		{Name: "x", Listen: ":1", Target: "a:99999"},
	} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestTCPRelayHalfCloseAndStats(t *testing.T) {
	f := New(Options{})
	defer f.Close()
	listen := freeAddr(t, "tcp")
	if err := f.Start(Rule{ID: 1, Name: "echo", Listen: listen, Target: echoTCP(t)}); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hello"))
	c.(*net.TCPConn).CloseWrite() // the target answers after our half-close
	got, _ := io.ReadAll(c)
	if string(got) != "hellobye" {
		t.Fatalf("got %q", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		s := f.Stats(1)
		if s.Running && s.Total == 1 && s.Active == 0 && s.BytesIn == 5 && s.BytesOut == 8 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stats = %+v", s)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStopClosesConnectionsAndBindErrors(t *testing.T) {
	f := New(Options{})
	defer f.Close()
	listen := freeAddr(t, "tcp")
	rule := Rule{ID: 1, Name: "echo", Listen: listen, Target: echoTCP(t)}
	if err := f.Start(rule); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("x"))
	buf := make([]byte, 1)
	io.ReadFull(c, buf)

	// The port is taken while the rule runs.
	if err := f.Start(Rule{ID: 2, Name: "clash", Listen: listen, Target: rule.Target}); err == nil {
		t.Fatal("second rule bound a taken port")
	}
	if s := f.Stats(2); s.Running || s.Error == "" {
		t.Fatalf("failed rule stats = %+v", s)
	}

	f.Stop(1)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(buf); err == nil {
		t.Fatal("connection survived Stop")
	}
	if s := f.Stats(1); s.Running {
		t.Fatalf("stats after stop = %+v", s)
	}
	// The port is free again.
	if err := f.Start(rule); err != nil {
		t.Fatal(err)
	}
}

func TestUDPSessionsPerClient(t *testing.T) {
	// The target answers each datagram with "re:" + payload.
	target, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := target.ReadFrom(buf)
			if err != nil {
				return
			}
			target.WriteTo(append([]byte("re:"), buf[:n]...), from)
		}
	}()
	f := New(Options{UDPIdle: 300 * time.Millisecond})
	defer f.Close()
	listen := freeAddr(t, "udp")
	if err := f.Start(Rule{ID: 1, Name: "udp", Protocol: "udp", Listen: listen, Target: target.LocalAddr().String()}); err != nil {
		t.Fatal(err)
	}
	ask := func(c net.Conn, msg string) string {
		c.Write([]byte(msg))
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 1500)
		n, err := c.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		return string(buf[:n])
	}
	a, _ := net.Dial("udp", listen)
	b, _ := net.Dial("udp", listen)
	defer a.Close()
	defer b.Close()
	for i := 0; i < 3; i++ {
		if got := ask(a, "a"); got != "re:a" {
			t.Fatalf("a got %q", got)
		}
		if got := ask(b, "b"); got != "re:b" {
			t.Fatalf("b got %q", got)
		}
	}
	if s := f.Stats(1); s.Total != 2 || s.Active != 2 || s.BytesIn != 6 || s.BytesOut != 24 {
		t.Fatalf("stats = %+v", s)
	}
	// Idle sessions are closed.
	deadline := time.Now().Add(3 * time.Second)
	for f.Stats(1).Active != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("sessions not closed: %+v", f.Stats(1))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := ask(a, "again"); got != "re:again" {
		t.Fatalf("after idle: %q", got)
	}
}

func TestUnreachableTarget(t *testing.T) {
	f := New(Options{DialTimeout: 500 * time.Millisecond})
	defer f.Close()
	listen := freeAddr(t, "tcp")
	if err := f.Start(Rule{ID: 1, Name: "dead", Listen: listen, Target: freeAddr(t, "tcp")}); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := io.ReadAll(c)
	if len(got) != 0 || err != nil && !strings.Contains(err.Error(), "reset") {
		t.Fatalf("got %q %v", got, err)
	}
}
