package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeAPITLSRequiresVerifiedCertificate(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	defer fixture.Close()
	pair := fixture.TLS.Certificates[0]
	private, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, files := range [][2]string{{cert, ""}, {"", key}, {key, cert}, {cert, filepath.Join(dir, "absent")}} {
		if _, err := apiTLSConfig(files[0], files[1]); err == nil {
			t.Fatal("invalid TLS configuration accepted")
		}
	}
	if cfg, err := apiTLSConfig("", ""); err != nil || cfg != nil {
		t.Fatal("existing HTTP configuration changed")
	}
	cfg, err := apiTLSConfig(cert, key)
	if err != nil || cfg.MinVersion < tls.VersionTLS12 {
		t.Fatal("TLS minimum missing", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			t.Error("request reached API without TLS")
		}
		w.WriteHeader(http.StatusNoContent)
	}), ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan error, 1)
	go func() { done <- server.Serve(tls.NewListener(listener, cfg)) }()
	defer func() { server.Close(); <-done }()
	pool := x509.NewCertPool()
	pool.AddCert(fixture.Certificate())
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	response, err := client.Get("https://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	client.CloseIdleConnections()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal(response.StatusCode)
	}
	untrusted := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool()}}}
	if _, err := untrusted.Get("https://" + listener.Addr().String()); err == nil {
		t.Fatal("untrusted API certificate accepted")
	}
}
