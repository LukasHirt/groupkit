// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Command server is a small CalDAV and CardDAV server for trying groupkit. It
// serves one account from a directory on disk.
//
// It speaks plain HTTP. Put a reverse proxy that terminates TLS in front of
// it before exposing it beyond localhost: Basic authentication sends the
// password with every request.
//
// The password comes from the environment variable GROUPKIT_PASSWORD, never
// from a flag. Run one process per data directory.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Server limits. They bound what one slow or hostile client can hold open.
const (
	// readHeaderTimeout is how long a client has to send the request headers.
	readHeaderTimeout = 10 * time.Second
	// readTimeout covers headers and body. Objects are at most 10 MiB, which
	// a slow link still sends within this time.
	readTimeout = 60 * time.Second
	// writeTimeout covers the response, including a large REPORT or GET.
	writeTimeout = 60 * time.Second
	// idleTimeout is how long a keep-alive connection may sit unused.
	idleTimeout = 120 * time.Second
	// maxHeaderBytes caps the request headers at 1 MiB.
	maxHeaderBytes = 1 << 20
	// shutdownTimeout is how long running requests get to finish on SIGINT or
	// SIGTERM.
	shutdownTimeout = 15 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stderr)
	stop()
	os.Exit(code)
}

// run starts the server and returns the exit status: 0 after a clean stop,
// 1 on a runtime failure, 2 for bad flags or configuration. It stops when ctx
// is done.
func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	var cfg config
	fs := newFlagSet(&cfg, stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cfg.Password = getenv(passwordEnv)
	if err := cfg.validate(); err != nil {
		_, _ = fmt.Fprintf(stderr, "server: %v\n", err)
		return 2
	}

	logger := slog.New(slog.NewTextHandler(stderr, nil))
	handler, closeStore, err := newHandler(cfg)
	if err != nil {
		logger.Error("start failed", "error", err)
		return 1
	}
	logger.Info("data directory opened", "dir", cfg.DataDir)
	logger.Info("default address book and calendar exist", "user", cfg.User)
	return serve(ctx, cfg, logger, handler, closeStore)
}

// newFlagSet defines the flags into cfg.
func newFlagSet(cfg *config, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.Addr, "addr", "127.0.0.1:8080", "listen `address`")
	fs.StringVar(&cfg.DataDir, "data", "./data", "data `directory` of the file store (one process per directory)")
	fs.StringVar(&cfg.User, "user", "demo", "name of the one account")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: server [flags]\n\n"+
			"A CalDAV and CardDAV server for one account, for trying groupkit.\n\n"+
			"The password is read from the environment variable %s (at least %d characters, at most %d bytes).\n"+
			"The server speaks plain HTTP with Basic authentication: TLS must be provided by a\n"+
			"reverse proxy before it is reachable beyond localhost.\n\nFlags:\n", passwordEnv, minPasswordLen, maxPasswordLen)
		fs.PrintDefaults()
	}
	return fs
}

// serve runs the HTTP server until ctx is done, then closes the store.
func serve(ctx context.Context, cfg config, logger *slog.Logger, handler http.Handler, closeStore func() error) int {
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		logger.Error("listen failed", "error", err)
		_ = closeStore()
		return 1
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		// "OPTIONS *" would otherwise be answered without credentials.
		DisableGeneralOptionsHandler: true,
	}
	logger.Info("listening", "addr", ln.Addr().String())

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	status := 0
	select {
	case err := <-serveErr:
		logger.Error("serve failed", "error", err)
		status = 1
	case <-ctx.Done():
		logger.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		if err := srv.Shutdown(sctx); err != nil {
			logger.Error("shutdown failed", "error", err)
			status = 1
		}
		cancel()
	}
	// The store is closed only after the server stopped taking requests.
	if err := closeStore(); err != nil {
		logger.Error("close the data directory failed", "error", err)
		status = 1
	}
	logger.Info("stopped")
	return status
}
