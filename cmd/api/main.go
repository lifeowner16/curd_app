package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"crud-api/internal/api"
	"crud-api/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func configuration() (string, string, error) {
	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", "", fmt.Errorf("HTTP_ADDR must be host:port: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", "", fmt.Errorf("HTTP_ADDR port must be an integer from 1 to 65535")
	}
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "data/app.db"
	}
	return address, path, nil
}

func run(ctx context.Context, logger *slog.Logger) (resultErr error) {
	address, path, err := configuration()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	startupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	database, err := store.Open(startupCtx, path)
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		if err := database.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close database: %w", err))
		}
	}()
	server := &http.Server{
		Addr:              address,
		Handler:           api.New(database, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	logger.Info("service listening", "address", listener.Addr().String(), "database", path)
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()
	select {
	case err := <-serveErrors:
		closeErr := server.Close()
		return errors.Join(fmt.Errorf("HTTP server stopped unexpectedly: %w", err), closeErr)
	case <-ctx.Done():
		logger.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return errors.Join(fmt.Errorf("shutdown HTTP server: %w", err), server.Close())
	}
	if err := <-serveErrors; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}
