package main

import (
	"crypto/tls"
	"errors"
)

// Optional native TLS also protects the proxy-to-API connection on deployments
// that require encryption on loopback. Both paths must be configured together.
func apiTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, errors.New("API_TLS_CERT_FILE and API_TLS_KEY_FILE must be configured together")
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, errors.New("API TLS certificate or private key is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}, nil
}
