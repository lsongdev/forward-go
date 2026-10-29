# forward-go

A small TCP/UDP port forwarder with a web UI: each rule relays a local port
to a target address, and shows whether it is running, its open connections
and its traffic.

- **TCP**: one connection to the target per client; each direction
  half-closes the other when it ends, so protocols that stop sending first
  keep working.
- **UDP**: one socket to the target per client address, so replies reach the
  client that caused them; a session closes after 60 seconds without
  traffic either way.
- A rule whose port is taken is refused when you add or enable it, and
  reported (not fatal) when it cannot start at launch.

One Go binary; rules live in a YAML configuration file. The UI is Preact + HTM loaded as native
ES modules, with no build step.

```sh
go run ./cmd/forward-go -listen 127.0.0.1:8080   # -config defaults to the user config dir
```

Set `FORWARD_GO_PASSWORD` to require HTTP basic authentication.

## As a panel module

forward-go follows the panel module contract (see OhMyServer's
`docs/module-contract.md`), so a panel shell can mount it beside other
modules:

```go
fwd, err := web.New(ctx, "forward.yaml", web.Options{})
fwd.Handler()                               // JSON API, relative routes
fwd.Assets()                                // UI with manifest.json
```

A panel that keeps module state in its own database passes a rule store
instead of the YAML file: `web.NewWithStore(ctx, st, opts)` takes any
`api.Store` (List, Get, Create, Update, Delete), reporting missing rules
and duplicates with `store.ErrNotFound` and `store.ErrConflict`. OhMyServer
does this, so the YAML file is only used when forward-go runs on its own.

## API

Responses are `{"data": …}`; errors are `{"error": {"code", "message"}}`.

| Method | Path | |
|---|---|---|
| GET | `/rules` | rules with `stats` (running, error, active, total, bytes_in, bytes_out) |
| POST | `/rules` | `{"name","protocol","listen","target","enabled"}`; 409 if the port cannot be bound |
| GET/PUT/DELETE | `/rules/{id}` | a failed rebind restores the previous rule |
| POST | `/rules/{id}/enable`, `/rules/{id}/disable` | |
| GET | `/summary` | counts for an overview |

## Configuration

Use `-config /path/to/forward.yaml` to select the file (default:
`forward-go/forward.yaml` under the user config directory). Changes from the UI
are saved immediately. Edit the file while the service is stopped, then restart.
Existing SQLite databases are not imported automatically.

```yaml
rules:
  - id: 1
    name: nas-ssh
    protocol: tcp
    listen: ":2222"
    target: "192.168.1.10:22"
    enabled: true
```

IDs may be omitted for new entries; the service assigns them on load. The file
also records timestamps and `next_id` when saved through the API.
