// Package forward relays TCP connections and UDP datagrams from local
// ports to targets. A Forwarder runs any number of rules and counts their
// traffic; it knows nothing about how rules are stored.
package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Rule forwards one local port to a target.
type Rule struct {
	ID       int64  `json:"id" yaml:"id"`
	Name     string `json:"name" yaml:"name"`
	Protocol string `json:"protocol" yaml:"protocol"` // tcp | udp
	// Listen is the local address, "host:port"; an empty host listens on
	// every interface. A bare port is accepted and normalized to ":port".
	Listen    string    `json:"listen" yaml:"listen"`
	Target    string    `json:"target" yaml:"target"` // "host:port"
	Enabled   bool      `json:"enabled" yaml:"enabled"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
}

// ErrInvalid marks a rule that fails validation.
var ErrInvalid = errors.New("invalid rule")

// Normalize trims and validates r, filling defaults.
func (r *Rule) Normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Protocol = strings.ToLower(strings.TrimSpace(r.Protocol))
	r.Listen = strings.TrimSpace(r.Listen)
	r.Target = strings.TrimSpace(r.Target)
	if r.Protocol == "" {
		r.Protocol = "tcp"
	}
	if _, err := strconv.Atoi(r.Listen); err == nil {
		r.Listen = ":" + r.Listen
	}
	switch {
	case r.Name == "":
		return fmt.Errorf("%w: name required", ErrInvalid)
	case r.Protocol != "tcp" && r.Protocol != "udp":
		return fmt.Errorf("%w: protocol must be tcp or udp", ErrInvalid)
	}
	if _, _, err := splitAddr(r.Listen, true); err != nil {
		return fmt.Errorf("%w: listen: %v", ErrInvalid, err)
	}
	if _, _, err := splitAddr(r.Target, false); err != nil {
		return fmt.Errorf("%w: target: %v", ErrInvalid, err)
	}
	return nil
}

func splitAddr(addr string, emptyHost bool) (string, int, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("port %q must be 1-65535", portText)
	}
	if host == "" && !emptyHost {
		return "", 0, errors.New("host required")
	}
	return host, port, nil
}

// Stats describes a rule's runtime state. Counters restart when the rule
// does.
type Stats struct {
	Running   bool      `json:"running"`
	Error     string    `json:"error,omitempty"` // why it is not running
	Active    int64     `json:"active"`          // open TCP connections or live UDP sessions
	Total     uint64    `json:"total"`           // connections or sessions since start
	BytesIn   uint64    `json:"bytes_in"`        // client → target
	BytesOut  uint64    `json:"bytes_out"`       // target → client
	StartedAt time.Time `json:"started_at,omitzero"`
}

// Options tunes a Forwarder; zero values select the defaults.
type Options struct {
	DialTimeout time.Duration // connecting to a TCP target (10s)
	UDPIdle     time.Duration // a UDP session with no traffic is closed (60s)
	Logger      *slog.Logger
}

// Forwarder runs rules.
type Forwarder struct {
	opts Options
	log  *slog.Logger

	mu      sync.Mutex
	running map[int64]*runner
	failed  map[int64]string // last start error of rules not running
}

// New returns a Forwarder with no rules running.
func New(opts Options) *Forwarder {
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.UDPIdle == 0 {
		opts.UDPIdle = 60 * time.Second
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Forwarder{opts: opts, log: log, running: map[int64]*runner{}, failed: map[int64]string{}}
}

// Start runs rule, replacing a running rule with the same ID. The port is
// bound before Start returns, so a taken port is reported at once; the
// error is also kept for Stats.
func (f *Forwarder) Start(rule Rule) error {
	if err := rule.Normalize(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if old := f.running[rule.ID]; old != nil {
		old.stop()
		delete(f.running, rule.ID)
	}
	r := &runner{rule: rule, opts: f.opts, log: f.log, conns: map[io.Closer]struct{}{}, started: time.Now()}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	var err error
	if rule.Protocol == "udp" {
		err = r.startUDP()
	} else {
		err = r.startTCP()
	}
	if err != nil {
		r.cancel()
		f.failed[rule.ID] = err.Error()
		return err
	}
	delete(f.failed, rule.ID)
	f.running[rule.ID] = r
	f.log.Info("forward: started", "rule", rule.Name, "protocol", rule.Protocol, "listen", rule.Listen, "target", rule.Target)
	return nil
}

// Stop stops a rule, closing its connections. Stopping a rule that is not
// running succeeds.
func (f *Forwarder) Stop(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.failed, id)
	if r := f.running[id]; r != nil {
		r.stop()
		delete(f.running, id)
		f.log.Info("forward: stopped", "rule", r.rule.Name)
	}
}

// Stats reports a rule's state.
func (f *Forwarder) Stats(id int64) Stats {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.running[id]
	if r == nil {
		return Stats{Error: f.failed[id]}
	}
	return Stats{
		Running: true, Active: r.active.Load(), Total: r.total.Load(),
		BytesIn: r.in.Load(), BytesOut: r.out.Load(), StartedAt: r.started,
	}
}

// Close stops every rule.
func (f *Forwarder) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, r := range f.running {
		r.stop()
		delete(f.running, id)
	}
}

// runner is one running rule.
type runner struct {
	rule    Rule
	opts    Options
	log     *slog.Logger
	ctx     context.Context
	cancel  context.CancelFunc
	started time.Time
	wg      sync.WaitGroup

	active         atomic.Int64
	total, in, out atomic.Uint64

	mu     sync.Mutex
	closer io.Closer              // the listener or packet socket
	conns  map[io.Closer]struct{} // open connections, closed on stop
}

func (r *runner) track(c io.Closer) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return false
	}
	r.conns[c] = struct{}{}
	return true
}

func (r *runner) untrack(c io.Closer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.conns, c)
}

// stop closes the socket and every connection, and waits for the
// goroutines to finish.
func (r *runner) stop() {
	r.cancel()
	r.mu.Lock()
	r.closer.Close()
	for c := range r.conns {
		c.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *runner) startTCP() error {
	ln, err := net.Listen("tcp", r.rule.Listen)
	if err != nil {
		return err
	}
	r.closer = ln
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				if r.ctx.Err() != nil {
					return
				}
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					continue
				}
				r.log.Error("forward: accept failed", "rule", r.rule.Name, "err", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if !r.track(conn) {
				conn.Close()
				return
			}
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				defer r.untrack(conn)
				r.relay(conn)
			}()
		}
	}()
	return nil
}

// relay copies between a client and the target until both directions end.
// Each direction half-closes the other side when its source is done, so
// protocols that shut down one direction first keep working.
func (r *runner) relay(client net.Conn) {
	defer client.Close()
	r.total.Add(1)
	r.active.Add(1)
	defer r.active.Add(-1)
	ctx, cancel := context.WithTimeout(r.ctx, r.opts.DialTimeout)
	target, err := (&net.Dialer{}).DialContext(ctx, "tcp", r.rule.Target)
	cancel()
	if err != nil {
		r.log.Warn("forward: target unreachable", "rule", r.rule.Name, "target", r.rule.Target, "err", err)
		return
	}
	if !r.track(target) {
		target.Close()
		return
	}
	defer r.untrack(target)
	defer target.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pipe(target, client, &r.in) }()
	go func() { defer wg.Done(); pipe(client, target, &r.out) }()
	wg.Wait()
}

func pipe(dst, src net.Conn, counter *atomic.Uint64) {
	io.Copy(countingWriter{dst, counter}, src)
	if c, ok := dst.(interface{ CloseWrite() error }); ok {
		c.CloseWrite()
	} else {
		dst.Close()
	}
}

type countingWriter struct {
	w io.Writer
	n *atomic.Uint64
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(uint64(n))
	return n, err
}

// maxUDPSessions bounds the clients one UDP rule tracks at once.
const maxUDPSessions = 4096

func (r *runner) startUDP() error {
	pc, err := net.ListenPacket("udp", r.rule.Listen)
	if err != nil {
		return err
	}
	r.closer = pc
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.serveUDP(pc)
	}()
	return nil
}

// serveUDP gives each client address its own socket to the target, so
// replies go back to the client that caused them. A session ends after
// UDPIdle without traffic.
func (r *runner) serveUDP(pc net.PacketConn) {
	var mu sync.Mutex
	sessions := map[string]*udpSession{}
	buf := make([]byte, 64<<10)
	for {
		n, client, err := pc.ReadFrom(buf)
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			continue
		}
		key := client.String()
		mu.Lock()
		s := sessions[key]
		if s == nil && len(sessions) < maxUDPSessions {
			conn, err := net.Dial("udp", r.rule.Target)
			if err != nil {
				mu.Unlock()
				r.log.Warn("forward: target unreachable", "rule", r.rule.Name, "target", r.rule.Target, "err", err)
				continue
			}
			if !r.track(conn) {
				mu.Unlock()
				conn.Close()
				return
			}
			s = &udpSession{conn: conn}
			s.touch()
			sessions[key] = s
			r.total.Add(1)
			r.active.Add(1)
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				r.udpReplies(pc, client, s)
				mu.Lock()
				delete(sessions, key)
				mu.Unlock()
				r.untrack(conn)
				conn.Close()
				r.active.Add(-1)
			}()
		}
		mu.Unlock()
		if s == nil {
			continue // too many sessions: drop
		}
		s.touch()
		if w, err := s.conn.Write(buf[:n]); err == nil {
			r.in.Add(uint64(w))
		}
	}
}

// udpSession is one client's socket to the target.
type udpSession struct {
	conn net.Conn
	last atomic.Int64 // unix nanoseconds of the last datagram either way
}

func (s *udpSession) touch() { s.last.Store(time.Now().UnixNano()) }

func (s *udpSession) idle() time.Duration { return time.Since(time.Unix(0, s.last.Load())) }

// udpReplies relays the target's replies to client until the session has
// been idle (no datagram either way) for UDPIdle, or the rule stops.
func (r *runner) udpReplies(pc net.PacketConn, client net.Addr, s *udpSession) {
	buf := make([]byte, 64<<10)
	for {
		s.conn.SetReadDeadline(time.Now().Add(r.opts.UDPIdle - s.idle()))
		n, err := s.conn.Read(buf)
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if s.idle() >= r.opts.UDPIdle {
					return
				}
				continue // the client sent something meanwhile
			}
			// ICMP port unreachable surfaces as a read error: the target
			// may come back, so keep the session until it idles out.
			continue
		}
		s.touch()
		if w, err := pc.WriteTo(buf[:n], client); err == nil {
			r.out.Add(uint64(w))
		}
	}
}
