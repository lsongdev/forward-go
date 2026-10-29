package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lsongdev/forward-go/forward"
)

func TestPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.yaml")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	r := forward.Rule{Name: "ssh", Listen: "2222", Target: "localhost:22"}
	if err := s.Create(ctx, &r); err != nil {
		t.Fatal(err)
	}
	created := r.CreatedAt
	r.Target = "localhost:23"
	if err := s.Update(ctx, &r); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, r.ID)
	if err != nil || got.Target != r.Target || !got.CreatedAt.Equal(created) {
		t.Fatalf("reloaded: %+v, %v", got, err)
	}
	duplicate := forward.Rule{Name: "ssh", Listen: "2223", Target: "localhost:22"}
	if err := s.Create(ctx, &duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := s.Delete(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted rule: %v", err)
	}
	if err := s.Create(ctx, &duplicate); err != nil {
		t.Fatal(err)
	}
	if duplicate.ID <= r.ID {
		t.Fatal("reused deleted ID")
	}
}

func TestConfigurationValidation(t *testing.T) {
	for _, content := range []string{
		"rules: [",
		"unknown: true\n",
		"rules:\n  - name: invalid\n",
		"rules: []\n---\nrules: []\n",
		"rules:\n  - id: 1\n  - id: 1\n",
	} {
		t.Run(content, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(context.Background(), path); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
}

func TestFailedSaveLeavesStateUnchanged(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.yaml")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// A directory at the destination makes the final rename fail.
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	r := forward.Rule{Name: "ssh", Listen: "2222", Target: "localhost:22"}
	if err := s.Create(ctx, &r); err == nil {
		t.Fatal("save succeeded")
	}
	rules, err := s.List(ctx)
	if err != nil || len(rules) != 0 || r.ID != 0 {
		t.Fatalf("failed save changed state: %+v, %+v, %v", r, rules, err)
	}
}
