package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lsongdev/forward-go/api"
	"github.com/lsongdev/forward-go/forward"
	"github.com/lsongdev/forward-go/store"
)

type env struct {
	t    *testing.T
	path string
	svc  *api.Service
	fwd  *forward.Forwarder
	h    http.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, path: filepath.Join(t.TempDir(), "forward.yaml")}
	e.start()
	return e
}

// start assembles the service on the env's configuration, as after a restart.
func (e *env) start() {
	st, err := store.Open(context.Background(), e.path)
	if err != nil {
		e.t.Fatal(err)
	}
	e.fwd = forward.New(forward.Options{})
	e.t.Cleanup(e.fwd.Close)
	e.svc = api.NewService(st, e.fwd, nil)
	if err := e.svc.Start(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	e.h = e.svc.Handler()
}

func (e *env) call(method, path string, body any, want int, out any) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, httptest.NewRequest(method, path, r))
	if w.Code != want {
		e.t.Fatalf("%s %s: %d, want %d: %s", method, path, w.Code, want, w.Body)
	}
	if out != nil {
		var envelope struct{ Data json.RawMessage }
		json.Unmarshal(w.Body.Bytes(), &envelope)
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			e.t.Fatalf("%s %s: %v: %s", method, path, err, w.Body)
		}
	}
}

func freePort(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func TestRulesLifecycle(t *testing.T) {
	e := newEnv(t)
	listen := freePort(t)
	var rule api.RuleStatus
	e.call("POST", "/rules", map[string]any{"name": "web", "listen": listen, "target": "127.0.0.1:9", "enabled": true}, 201, &rule)
	if rule.ID == 0 || rule.Protocol != "tcp" || !rule.Stats.Running {
		t.Fatalf("created %+v", rule)
	}
	if _, err := net.Dial("tcp", listen); err != nil {
		t.Fatalf("rule not listening: %v", err)
	}
	// Same name, or same protocol and address: conflict.
	e.call("POST", "/rules", map[string]any{"name": "web", "listen": freePort(t), "target": "a:1"}, 409, nil)
	e.call("POST", "/rules", map[string]any{"name": "other", "listen": listen, "target": "a:1"}, 409, nil)
	e.call("POST", "/rules", map[string]any{"name": "bad", "listen": "x", "target": "a:1"}, 422, nil)

	// A port someone else holds is refused and nothing is stored.
	taken, _ := net.Listen("tcp", "127.0.0.1:0")
	defer taken.Close()
	e.call("POST", "/rules", map[string]any{"name": "taken", "listen": taken.Addr().String(), "target": "a:1", "enabled": true}, 409, nil)
	var rules []api.RuleStatus
	e.call("GET", "/rules", nil, 200, &rules)
	if len(rules) != 1 {
		t.Fatalf("rules = %+v", rules)
	}

	// Moving to a taken port restores the previous rule.
	e.call("PUT", "/rules/1", map[string]any{"name": "web", "listen": taken.Addr().String(), "target": "a:1", "enabled": true}, 409, nil)
	e.call("GET", "/rules/1", nil, 200, &rule)
	if rule.Listen != listen || !rule.Stats.Running {
		t.Fatalf("after failed move: %+v", rule)
	}
	moved := freePort(t)
	e.call("PUT", "/rules/1", map[string]any{"name": "web", "listen": moved, "target": "a:1", "enabled": true}, 200, &rule)
	if _, err := net.Dial("tcp", listen); err == nil {
		t.Fatal("old port still served")
	}

	e.call("POST", "/rules/1/disable", nil, 200, &rule)
	if rule.Enabled || rule.Stats.Running {
		t.Fatalf("disabled %+v", rule)
	}
	// Disabled rules stay off after a restart; enabled ones come back.
	e.fwd.Close()
	e.start()
	e.call("GET", "/rules/1", nil, 200, &rule)
	if rule.Stats.Running {
		t.Fatal("disabled rule started")
	}
	e.call("POST", "/rules/1/enable", nil, 200, &rule)
	e.fwd.Close()
	e.start()
	var sum api.Summary
	e.call("GET", "/summary", nil, 200, &sum)
	if sum.Rules != 1 || sum.Running != 1 {
		t.Fatalf("summary %+v", sum)
	}
	e.call("DELETE", "/rules/1", nil, 204, nil)
	e.call("GET", "/rules/1", nil, 404, nil)
	if _, err := net.Dial("tcp", moved); err == nil {
		t.Fatal("deleted rule still served")
	}
}

// A stored rule whose port is taken at startup is reported, not fatal.
func TestStartReportsTakenPorts(t *testing.T) {
	e := newEnv(t)
	listen := freePort(t)
	e.call("POST", "/rules", map[string]any{"name": "web", "listen": listen, "target": "a:1", "enabled": true}, 201, nil)
	e.fwd.Close()
	taken, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	e.start()
	var rule api.RuleStatus
	e.call("GET", "/rules/1", nil, 200, &rule)
	if rule.Stats.Running || rule.Stats.Error == "" {
		t.Fatalf("rule %+v", rule)
	}
	var sum api.Summary
	e.call("GET", "/summary", nil, 200, &sum)
	if sum.Failed != 1 {
		t.Fatalf("summary %+v", sum)
	}
}
