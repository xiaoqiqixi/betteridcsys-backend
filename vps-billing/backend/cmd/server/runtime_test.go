package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func runtimeListener(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestHTTPShutdownWaitsForActiveHandler(t *testing.T) {
	l := runtimeListener(t)
	entered, release, draining := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			_, _ = io.WriteString(w, "completed")
		case <-r.Context().Done():
		}
	})}
	server.RegisterOnShutdown(func() { close(draining) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	t.Cleanup(func() { _ = server.Close() })
	done := make(chan error, 1)
	go func() {
		done <- runHTTPServer(ctx, server, l, time.Second, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	}()
	response := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		r, err := client.Get("http://" + l.Addr().String())
		if err == nil {
			defer r.Body.Close()
			body, readErr := io.ReadAll(r.Body)
			err = readErr
			if err == nil && (r.StatusCode != 200 || string(body) != "completed") {
				err = errors.New("in-flight response was not completed")
			}
		}
		response <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case <-draining:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not begin")
	}
	select {
	case err := <-done:
		t.Fatal("server returned before its active handler finished", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-response; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHTTPShutdownDeadlineClosesActiveConnections(t *testing.T) {
	l := runtimeListener(t)
	entered, exited := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(exited)
	})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	t.Cleanup(func() { _ = server.Close() })
	done := make(chan error, 1)
	go func() {
		done <- runHTTPServer(ctx, server, l, 30*time.Millisecond, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	}()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		c := &http.Client{Timeout: time.Second}
		if r, err := c.Get("http://" + l.Addr().String()); err == nil {
			_ = r.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expected an explicit failed drain", err)
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("deadline did not cancel the active connection")
	}
	<-requestDone
}

func TestHTTPTransportDiagnosticDoesNotCopyPayload(t *testing.T) {
	var output bytes.Buffer
	w := transportLogWriter{slog.New(slog.NewJSONHandler(&output, nil))}
	secret := []byte("panic: PRIVATE_PASSWORD?token=SECRET\nAuthorization: PRIVATE\n")
	if n, err := w.Write(secret); n != len(secret) || err != nil {
		t.Fatal(n, err)
	}
	if strings.Contains(output.String(), "PRIVATE") || strings.Contains(output.String(), "SECRET") || !strings.Contains(output.String(), "http.transport_error") {
		t.Fatal("unsafe or missing transport event", output.String())
	}
}
