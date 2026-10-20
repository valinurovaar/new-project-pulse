// Command projectpulse starts the HTTP API and background workers.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/valinurovaar/new-project-pulse/internal/api"
	"github.com/valinurovaar/new-project-pulse/internal/auth"
	"github.com/valinurovaar/new-project-pulse/internal/config"
	"github.com/valinurovaar/new-project-pulse/internal/postgres"
	"github.com/valinurovaar/new-project-pulse/internal/worker"
	"github.com/valinurovaar/new-project-pulse/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log, os.Args[1:]); err != nil {
		log.Error("projectpulse stopped with error", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, args []string) error {
	cfg, e := config.Load(args)
	if e != nil {
		return fmt.Errorf("configuration: %w", e)
	}
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e = migrations.Apply(root, cfg.DatabaseURI); e != nil {
		return fmt.Errorf("migrations: %w", e)
	}
	db, e := postgres.Open(root, cfg.DatabaseURI)
	if e != nil {
		return fmt.Errorf("database: %w", e)
	}
	defer db.Close()
	inspector, e := worker.NewInspector(cfg.InspectorAddress, 10*time.Second)
	if e != nil {
		return fmt.Errorf("inspector configuration: %w", e)
	}
	pool := worker.New(db, inspector, cfg.WorkerCount, cfg.CheckInterval, log)
	srv := &http.Server{Addr: cfg.RunAddress, Handler: api.New(db, auth.NewManager(cfg.AuthSecret, 24*time.Hour)).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	listener, e := net.Listen("tcp", cfg.RunAddress)
	if e != nil {
		return fmt.Errorf("listen %s: %w", cfg.RunAddress, e)
	}
	pool.Run(root)
	serverDone := make(chan error, 1)
	go func() {
		log.Info("server started", "address", listener.Addr().String())
		e := srv.Serve(listener)
		if e == http.ErrServerClosed {
			e = nil
		}
		serverDone <- e
		if e != nil {
			stop()
		}
	}()
	<-root.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	shutdownErr := srv.Shutdown(ctx)
	pool.Wait()
	serveErr := <-serverDone
	if shutdownErr != nil {
		return fmt.Errorf("HTTP shutdown: %w", shutdownErr)
	}
	if serveErr != nil {
		return fmt.Errorf("HTTP server: %w", serveErr)
	}
	log.Info("server stopped")
	return nil
}
