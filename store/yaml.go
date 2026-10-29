// Package store persists forwarding rules in a YAML configuration file.
package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/lsongdev/forward-go/forward"
	"gopkg.in/yaml.v3"
)

var ErrNotFound = errors.New("rule not found")
var ErrConflict = errors.New("conflict")

type config struct {
	NextID int64          `yaml:"next_id"`
	Rules  []forward.Rule `yaml:"rules"`
}

// YAML holds a single configuration file. Changes are atomically written
// before they become visible to readers. Use one instance per file.
type YAML struct {
	mu   sync.RWMutex
	path string
	data config
}

func Open(ctx context.Context, path string) (*YAML, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := &YAML{path: path, data: config{NextID: 1, Rules: []forward.Rule{}}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&s.data); err != nil {
		return nil, fmt.Errorf("read configuration: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("configuration must contain one YAML document")
	}
	ids := map[int64]bool{}
	for i := range s.data.Rules {
		r := &s.data.Rules[i]
		if r.ID < 0 || (r.ID > 0 && ids[r.ID]) {
			return nil, fmt.Errorf("invalid or duplicate rule id %d", r.ID)
		}
		if r.ID > 0 {
			ids[r.ID] = true
		}
		if r.ID >= s.data.NextID {
			s.data.NextID = r.ID + 1
		}
	}
	if s.data.NextID < 1 {
		s.data.NextID = 1
	}
	for i := range s.data.Rules {
		r := &s.data.Rules[i]
		if r.ID == 0 {
			r.ID = s.data.NextID
			s.data.NextID++
		}
		if err := r.Normalize(); err != nil {
			return nil, fmt.Errorf("rule %d: %w", r.ID, err)
		}
		if err := unique(s.data.Rules[:i], *r); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *YAML) Close() error { return nil }

func (s *YAML) List(ctx context.Context) ([]forward.Rule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rules := append([]forward.Rule{}, s.data.Rules...)
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	return rules, nil
}

func (s *YAML) Get(ctx context.Context, id int64) (forward.Rule, error) {
	if err := ctx.Err(); err != nil {
		return forward.Rule{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.data.Rules {
		if r.ID == id {
			return r, nil
		}
	}
	return forward.Rule{}, ErrNotFound
}

func unique(rules []forward.Rule, r forward.Rule) error {
	for _, old := range rules {
		if old.ID == r.ID {
			continue
		}
		if old.Name == r.Name {
			return fmt.Errorf("%w: a rule with this name exists", ErrConflict)
		}
		if old.Protocol == r.Protocol && old.Listen == r.Listen {
			return fmt.Errorf("%w: another rule already listens on this address", ErrConflict)
		}
	}
	return nil
}

func (s *YAML) Create(ctx context.Context, r *forward.Rule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := *r
	next.ID = s.data.NextID
	if err := next.Normalize(); err != nil {
		return err
	}
	if err := unique(s.data.Rules, next); err != nil {
		return err
	}
	next.CreatedAt = time.Now().UTC()
	next.UpdatedAt = next.CreatedAt
	data := config{NextID: next.ID + 1, Rules: append(append([]forward.Rule{}, s.data.Rules...), next)}
	if err := s.save(ctx, data); err != nil {
		return err
	}
	*r = next
	return nil
}

func (s *YAML) Update(ctx context.Context, r *forward.Rule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := *r
	if err := next.Normalize(); err != nil {
		return err
	}
	if err := unique(s.data.Rules, next); err != nil {
		return err
	}
	data := config{NextID: s.data.NextID, Rules: append([]forward.Rule{}, s.data.Rules...)}
	for i, old := range data.Rules {
		if old.ID != r.ID {
			continue
		}
		next.CreatedAt = old.CreatedAt
		next.UpdatedAt = time.Now().UTC()
		data.Rules[i] = next
		if err := s.save(ctx, data); err != nil {
			return err
		}
		*r = next
		return nil
	}
	return ErrNotFound
}

func (s *YAML) Delete(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := config{NextID: s.data.NextID, Rules: []forward.Rule{}}
	for _, r := range s.data.Rules {
		if r.ID != id {
			data.Rules = append(data.Rules, r)
		}
	}
	return s.save(ctx, data)
}

func (s *YAML) save(ctx context.Context, data config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".forward-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(encoded); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), s.path); err != nil {
		return err
	}
	s.data = data
	return nil
}
