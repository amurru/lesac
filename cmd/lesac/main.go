package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	httpadapter "github.com/amurru/lesac/internal/adapters/http"
	"github.com/amurru/lesac/internal/adapters/metadata/sqlite"
	"github.com/amurru/lesac/internal/adapters/storage/filesystem"
	"github.com/amurru/lesac/internal/config"
	"github.com/amurru/lesac/internal/runtime"
	"github.com/amurru/lesac/internal/usecase"
)

func main() {
	if err := run(); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.toml", "Path to TOML configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	metadataStore, err := sqlite.NewStore(cfg.Metadata.SQLitePath)
	if err != nil {
		return err
	}
	defer metadataStore.Close()

	blobStore, err := filesystem.NewStore(cfg.Storage.RootDir)
	if err != nil {
		return err
	}

	service := usecase.NewService(metadataStore, blobStore, nil, nil)
	handler := httpadapter.NewHandler(service, cfg.Uploads.MaxUploadSize)

	server := &http.Server{
		Addr:         cfg.Server.BindAddress,
		Handler:      handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := log.New(os.Stdout, "", log.LstdFlags)

	if cfg.Cleanup.Enabled {
		runtime.StartSweeper(ctx, logger, cfg.Cleanup.Interval, cfg.Cleanup.BatchSize, service)
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Printf("listening on %s", cfg.Server.BindAddress)
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-serverErr
	}
}
