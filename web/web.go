// Package web assembles the port forwarder as a panel module: the
// forwarder, its SQLite store, the JSON API and the browser UI (native ES
// modules in ui/, no build step).
//
// A Forward value satisfies the panel module contract without depending on
// any panel: Handler serves the API with relative routes and Assets holds
// the UI with manifest.json at its root. The standalone command and a panel
// build it the same way, through New.
package web

import (
	"context"
	"database/sql"
	"embed"
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
	store     *store.SQLite
}

// New migrates the forward_ tables in db (shareable with other modules),
// and starts the enabled rules. The caller owns db.
func New(ctx context.Context, db *sql.DB, opts Options) (*Forward, error) {
	st, err := store.New(ctx, db)
	if err != nil {
		return nil, err
	}
	return NewWithStore(ctx, st, opts)
}

// NewWithStore assembles the forwarder on st, which it closes on Close.
func NewWithStore(ctx context.Context, st *store.SQLite, opts Options) (*Forward, error) {
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

// Close stops every rule. A database passed to New stays open.
func (f *Forward) Close() error {
	f.forwarder.Close()
	return f.store.Close()
}
