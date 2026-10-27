package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// net/http diagnostics can contain untrusted request text and panic values.
// Keep a transport-failure signal without copying that text into the journal.
type transportLogWriter struct{ logger *slog.Logger }

func (w transportLogWriter) Write(p []byte) (int, error) {
	w.logger.Error("HTTP transport failure", "event", "http.transport_error")
	return len(p), nil
}

func runHTTPServer(ctx context.Context, server *http.Server, listener net.Listener, drain time.Duration, logger *slog.Logger) error {
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		logger.Error("HTTP serving failed", "event", "server.serve_failed")
		return err
	case <-ctx.Done():
	}
	logger.Info("HTTP drain started", "event", "server.draining")
	drainCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	err := server.Shutdown(drainCtx)
	if err != nil {
		// Shutdown's deadline does not close active connections. Bound shutdown
		// and cancel their contexts before releasing the database connection pool.
		_ = server.Close()
	}
	serveErr := <-done
	if err != nil {
		logger.Error("HTTP drain deadline exceeded", "event", "server.drain_failed")
		return err
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		logger.Error("HTTP serving failed during drain", "event", "server.serve_failed")
		return serveErr
	}
	logger.Info("HTTP drain completed", "event", "server.stopped")
	return nil
}
