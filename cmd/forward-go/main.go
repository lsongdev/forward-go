// Command forward-go serves the port forwarder on its own: the rules run
// in this process, and the UI and API manage them. A panel shell can mount
// the same module (web.New) beside others instead.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lsongdev/forward-go/store"
	"github.com/lsongdev/forward-go/web"
)

const usage = `usage: forward-go [flags]

Serves the port forwarder. Set FORWARD_GO_PASSWORD to require HTTP basic
authentication (any user name).

Flags:
`

func main() {
	flag.Usage = func() { fmt.Fprint(flag.CommandLine.Output(), usage); flag.PrintDefaults() }
	listen := flag.String("listen", "127.0.0.1:8080", "address for the UI and API")
	configPath := flag.String("config", defaultConfigPath(), "YAML configuration path")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	if err := run(*listen, *configPath, logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(listen, configPath string, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return err
	}
	st, err := store.Open(ctx, configPath)
	if err != nil {
		return err
	}
	fwd, err := web.NewWithStore(ctx, st, web.Options{Logger: logger})
	if err != nil {
		st.Close()
		return err
	}
	defer fwd.Close()

	password := os.Getenv("FORWARD_GO_PASSWORD")
	if password == "" && !isLoopback(listen) {
		logger.Warn("serving without authentication on a non-loopback address; set FORWARD_GO_PASSWORD")
	}
	srv := &http.Server{Addr: listen, Handler: fwd.Standalone(password), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logger.Info("forward-go listening", "url", "http://"+listen, "config", configPath)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

func isLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "forward.yaml"
	}
	return filepath.Join(dir, "forward-go", "forward.yaml")
}
