// Package web assembles the port forwarder as a panel module: the
// forwarder, a rule store (a YAML file of its own, or one a panel
// provides), the JSON API and the browser UI (native ES modules in ui/, no
// build step).
//
// A Forward value satisfies the panel module contract without depending on
// any panel: Handler serves the API with relative routes and Assets holds
// the UI with manifest.json at its root. The standalone command and a panel
// build it the same way, through New or NewWithStore.
package web

import (
	"context"
	"embed"
	"io"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/lsongdev/forward-go/api"
	"github.com/lsongdev/forward-go/forward"
	"github.com/lsongdev/forward-go/store"
)

//go:embed ui
var embedded embed.FS

// UI is the module's browser assets; manifest.json describes them.
var UI = func() fs.FS {
	sub, err := fs.Sub(embedded, "ui")
	if err != nil {
		panic(err) // the embed directive guarantees the directory exists
	}
	return sub
}()

// Options configures the forwarder.
type Options struct {
	Forward forward.Options
	Logger  *slog.Logger
}

// Forward is an assembled port forwarder.
type Forward struct {
	Service   *api.Service
	forwarder *forward.Forwarder
	store     api.Store
}

// New opens the YAML configuration at path and starts enabled rules.
func New(ctx context.Context, path string, opts Options) (*Forward, error) {
	st, err := store.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return NewWithStore(ctx, st, opts)
}

// NewWithStore assembles the forwarder on st, such as a panel's database,
// and closes it on Close if it is an io.Closer. st reports missing rules
// and duplicates with store.ErrNotFound and store.ErrConflict.
func NewWithStore(ctx context.Context, st api.Store, opts Options) (*Forward, error) {
	if opts.Forward.Logger == nil {
		opts.Forward.Logger = opts.Logger
	}
	fwd := forward.New(opts.Forward)
	service := api.NewService(st, fwd, opts.Logger)
	if err := service.Start(ctx); err != nil {
		fwd.Close()
		return nil, err
	}
	return &Forward{Service: service, forwarder: fwd, store: st}, nil
}

// Handler serves the module's API with relative routes.
func (f *Forward) Handler() http.Handler { return f.Service.Handler() }

// Assets returns the browser UI; manifest.json is at its root.
func (f *Forward) Assets() fs.FS { return UI }

// Close stops every rule. Configuration changes are persisted immediately.
func (f *Forward) Close() error {
	f.forwarder.Close()
	if closer, ok := f.store.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
