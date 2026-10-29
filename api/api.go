// Package api keeps the stored rules and the running forwarder in
// agreement, and serves them as a JSON API with relative routes.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"

	"github.com/lsongdev/forward-go/forward"
	"github.com/lsongdev/forward-go/store"
)

// Store persists rules; store.YAML implements it, and a panel may provide
// its own. Missing rules are store.ErrNotFound, and a duplicate name or
// listen address store.ErrConflict.
type Store interface {
	List(ctx context.Context) ([]forward.Rule, error)
	Get(ctx context.Context, id int64) (forward.Rule, error)
	Create(ctx context.Context, r *forward.Rule) error
	Update(ctx context.Context, r *forward.Rule) error
	Delete(ctx context.Context, id int64) error
}

// ErrListen reports an enabled rule whose port could not be bound; the
// change is not saved.
var ErrListen = errors.New("cannot listen")

// RuleStatus is a rule with its runtime state.
type RuleStatus struct {
	forward.Rule
	Stats forward.Stats `json:"stats"`
}

// Summary counts rules for an overview.
type Summary struct {
	Rules   int   `json:"rules"`
	Running int   `json:"running"`
	Failed  int   `json:"failed"` // enabled but not running
	Active  int64 `json:"active"` // open connections and sessions
}

// Service applies rule changes to the store and the forwarder together.
type Service struct {
	store Store
	fwd   *forward.Forwarder
	log   *slog.Logger
	mu    sync.Mutex // serializes changes
}

// NewService returns a service; call Start to run the stored rules.
func NewService(st Store, fwd *forward.Forwarder, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: st, fwd: fwd, log: log}
}

// Start runs every enabled rule. A rule that cannot bind its port is
// reported in its status; the others run.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rules, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if r.Enabled {
			if err := s.fwd.Start(r); err != nil {
				s.log.Error("forward: rule did not start", "rule", r.Name, "listen", r.Listen, "err", err)
			}
		}
	}
	return nil
}

func (s *Service) status(r forward.Rule) RuleStatus {
	return RuleStatus{Rule: r, Stats: s.fwd.Stats(r.ID)}
}

// List returns every rule with its state.
func (s *Service) List(ctx context.Context) ([]RuleStatus, error) {
	rules, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RuleStatus, len(rules))
	for i, r := range rules {
		out[i] = s.status(r)
	}
	return out, nil
}

// Summary counts the rules and their state.
func (s *Service) Summary(ctx context.Context) (Summary, error) {
	rules, err := s.List(ctx)
	var sum Summary
	for _, r := range rules {
		sum.Rules++
		switch {
		case r.Stats.Running:
			sum.Running++
			sum.Active += r.Stats.Active
		case r.Enabled:
			sum.Failed++
		}
	}
	return sum, err
}

// Get returns one rule with its state.
func (s *Service) Get(ctx context.Context, id int64) (RuleStatus, error) {
	r, err := s.store.Get(ctx, id)
	if err != nil {
		return RuleStatus{}, err
	}
	return s.status(r), nil
}

// Create stores r and, if enabled, starts it. A rule whose port cannot be
// bound is not stored.
func (s *Service) Create(ctx context.Context, r forward.Rule) (RuleStatus, error) {
	if err := r.Normalize(); err != nil {
		return RuleStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.Create(ctx, &r); err != nil {
		return RuleStatus{}, err
	}
	if r.Enabled {
		if err := s.fwd.Start(r); err != nil {
			s.fwd.Stop(r.ID)
			s.store.Delete(context.WithoutCancel(ctx), r.ID)
			return RuleStatus{}, fmt.Errorf("%w on %s: %v", ErrListen, r.Listen, err)
		}
	}
	return s.status(r), nil
}

// Update replaces a rule and restarts it. If the new rule cannot bind its
// port, the previous rule is restored.
func (s *Service) Update(ctx context.Context, id int64, r forward.Rule) (RuleStatus, error) {
	if err := r.Normalize(); err != nil {
		return RuleStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.store.Get(ctx, id)
	if err != nil {
		return RuleStatus{}, err
	}
	r.ID, r.CreatedAt = id, old.CreatedAt
	if err := s.store.Update(ctx, &r); err != nil {
		return RuleStatus{}, err
	}
	s.fwd.Stop(id) // release the port before binding it again
	if r.Enabled {
		if err := s.fwd.Start(r); err != nil {
			s.store.Update(context.WithoutCancel(ctx), &old)
			s.fwd.Stop(id)
			if old.Enabled {
				s.fwd.Start(old)
			}
			return RuleStatus{}, fmt.Errorf("%w on %s: %v", ErrListen, r.Listen, err)
		}
	}
	return s.status(r), nil
}

// SetEnabled starts or stops a rule. Enabling fails, and changes nothing,
// when the port cannot be bound.
func (s *Service) SetEnabled(ctx context.Context, id int64, enabled bool) (RuleStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.store.Get(ctx, id)
	if err != nil {
		return RuleStatus{}, err
	}
	if enabled {
		if err := s.fwd.Start(r); err != nil {
			s.fwd.Stop(id)
			return RuleStatus{}, fmt.Errorf("%w on %s: %v", ErrListen, r.Listen, err)
		}
	} else {
		s.fwd.Stop(id)
	}
	r.Enabled = enabled
	if err := s.store.Update(ctx, &r); err != nil {
		return RuleStatus{}, err
	}
	return s.status(r), nil
}

// Delete stops and removes a rule.
func (s *Service) Delete(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fwd.Stop(id)
	return s.store.Delete(ctx, id)
}

// Handler serves the API. Successful responses are {"data": ...}; failures
// are {"error": {"code": ..., "message": ...}} with a matching status.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rules", func(w http.ResponseWriter, r *http.Request) {
		rules, err := s.List(r.Context())
		respond(w, http.StatusOK, rules, err)
	})
	mux.HandleFunc("POST /rules", func(w http.ResponseWriter, r *http.Request) {
		var rule forward.Rule
		if decode(w, r, &rule) {
			item, err := s.Create(r.Context(), rule)
			respond(w, http.StatusCreated, item, err)
		}
	})
	mux.HandleFunc("GET /rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := pathID(w, r); ok {
			item, err := s.Get(r.Context(), id)
			respond(w, http.StatusOK, item, err)
		}
	})
	mux.HandleFunc("PUT /rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		var rule forward.Rule
		if id, ok := pathID(w, r); ok && decode(w, r, &rule) {
			item, err := s.Update(r.Context(), id, rule)
			respond(w, http.StatusOK, item, err)
		}
	})
	for action, enabled := range map[string]bool{"enable": true, "disable": false} {
		mux.HandleFunc("POST /rules/{id}/"+action, func(w http.ResponseWriter, r *http.Request) {
			if id, ok := pathID(w, r); ok {
				item, err := s.SetEnabled(r.Context(), id, enabled)
				respond(w, http.StatusOK, item, err)
			}
		})
	}
	mux.HandleFunc("DELETE /rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if id, ok := pathID(w, r); ok {
			respond(w, http.StatusNoContent, nil, s.Delete(r.Context(), id))
		}
	})
	mux.HandleFunc("GET /summary", func(w http.ResponseWriter, r *http.Request) {
		sum, err := s.Summary(r.Context())
		respond(w, http.StatusOK, sum, err)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such API endpoint")
	})
	return mux
}

func respond(w http.ResponseWriter, status int, data any, err error) {
	switch {
	case err == nil:
	case errors.Is(err, forward.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	case errors.Is(err, store.ErrConflict), errors.Is(err, ErrListen):
		writeError(w, http.StatusConflict, "conflict", err.Error())
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if status == http.StatusNoContent || data == nil {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, map[string]any{"data": data})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid id")
		return 0, false
	}
	return id, true
}
