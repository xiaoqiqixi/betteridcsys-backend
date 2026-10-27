package billing

import (
	"aster/billing/plugins/smtp"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A subprocess initializes Go's normal system-root cache using only this
// ephemeral CA. Production transport APIs have no custom trust/SkipVerify knob.
func TestAdminSMTPActualTLSDelivery(t *testing.T) {
	if os.Getenv("ASTER_SMTP_HTTP_FIXTURE_CHILD") != "1" {
		directory := t.TempDir()
		writeSMTPHTTPFixtureCertificate(t, directory)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, executable, "-test.run=^TestAdminSMTPActualTLSDelivery$", "-test.v")
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.HasPrefix(key, "ASTER_SMTP_HTTP_FIXTURE_") && key != "SSL_CERT_FILE" && key != "SSL_CERT_DIR" {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "ASTER_SMTP_HTTP_FIXTURE_CHILD=1", "ASTER_SMTP_HTTP_FIXTURE_DIR="+directory,
			"SSL_CERT_FILE="+filepath.Join(directory, "ca.pem"), "SSL_CERT_DIR="+filepath.Join(directory, "empty-roots"))
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated SMTP HTTP integration failed: %v\n%s", err, output)
		}
		return
	}

	directory := os.Getenv("ASTER_SMTP_HTTP_FIXTURE_DIR")
	certificate, err := tls.LoadX509KeyPair(filepath.Join(directory, "server.pem"), filepath.Join(directory, "server.key"))
	if err != nil {
		t.Fatal("ephemeral fixture certificate unavailable")
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan smtpHTTPFixtureResult, 1)
	go func() { result <- serveSMTPHTTPFixture(listener) }()
	_, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cipher, err := newSMTPCipher(key)
	clear(key)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemory()
	app := NewApp(store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), SMTPKey: cipher})
	app.SMTP.sender = smtp.NewClient() // Explicit transport fixture; production has no compiled fallback.
	api := httptest.NewServer(app.Handler())
	defer api.Close()
	admin := client(t, api.URL, "admin@aster.local", "DemoAdmin!2026")
	status, response := admin.request("GET", "/api/v1/admin/email-settings", nil, "")
	if status != 200 {
		t.Fatalf("load email settings status %d", status)
	}
	revision := response["data"].(map[string]any)["revision"]
	status, response = admin.request("POST", "/api/v1/admin/email-settings", map[string]any{
		"revision": revision, "enabled": true, "host": "127.0.0.1", "port": port, "tls_mode": "implicit_tls",
		"username": "http-fixture-user", "password": "HttpFixtureOnly!2026",
		"from_address": "fixture-sender@example.test", "from_name": "SMTP fixture sender",
	}, "")
	if status != 200 {
		t.Fatalf("save email settings status %d", status)
	}
	settings := response["data"].(map[string]any)
	if settings["username_configured"] != true || settings["password_configured"] != true || settings["key_configured"] != true {
		t.Fatal("configured credential flags missing")
	}
	record, err := store.EmailSettings(t.Context())
	if err != nil || !strings.HasPrefix(record.CredentialsCiphertext, "v1:") {
		t.Fatal("saved SMTP credentials were not encrypted")
	}
	recordJSON, _ := json.Marshal(record)
	responseJSON, _ := json.Marshal(response)
	for _, value := range []string{"http-fixture-user", "HttpFixtureOnly!2026"} {
		if bytes.Contains(recordJSON, []byte(value)) || bytes.Contains(responseJSON, []byte(value)) {
			t.Fatal("SMTP authentication material appeared in persistence/API output")
		}
	}
	status, response = admin.request("POST", "/api/v1/admin/email-settings/test", map[string]any{
		"revision": settings["revision"], "to": "fixture-recipient@example.test",
	}, "")
	if status != 200 || response["data"].(map[string]any)["status"] != "accepted" {
		t.Fatalf("real SMTP test did not return accepted, status %d", status)
	}
	select {
	case delivered := <-result:
		if delivered.err != nil || !delivered.tls || !delivered.authenticated {
			t.Fatal("local verified-TLS/authenticated SMTP fixture failed")
		}
		message, err := mail.ReadMessage(bytes.NewReader(delivered.message))
		if err != nil {
			t.Fatal("local SMTP fixture received invalid message")
		}
		to, err := mail.ParseAddress(message.Header.Get("To"))
		if err != nil || to.Address != "fixture-recipient@example.test" || message.Header.Get("Subject") != "Aster SMTP configuration test" {
			t.Fatal("local SMTP fixture received unexpected message identity")
		}
		text, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
		if err != nil || !bytes.Contains(text, []byte("administrator-requested SMTP configuration test")) {
			t.Fatal("local SMTP fixture received unexpected message content")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local SMTP fixture did not capture accepted message")
	}
	audits, err := store.Audits(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	accepted := false
	for _, entry := range audits {
		accepted = accepted || entry.Action == "email.test_accepted"
	}
	if !accepted {
		t.Fatal("actual SMTP acceptance was not audited")
	}
}

type smtpHTTPFixtureResult struct {
	message       []byte
	tls           bool
	authenticated bool
	err           error
}

func serveSMTPHTTPFixture(listener net.Listener) (result smtpHTTPFixtureResult) {
	result.err = errors.New("local SMTP fixture incomplete")
	conn, err := listener.Accept()
	if err != nil {
		return result
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	tlsConn, ok := conn.(*tls.Conn)
	if !ok || tlsConn.Handshake() != nil {
		return result
	}
	result.tls = tlsConn.ConnectionState().HandshakeComplete
	text := textproto.NewConn(conn)
	if text.PrintfLine("220 isolated HTTP SMTP fixture") != nil {
		return result
	}
	for {
		line, err := text.ReadLine()
		if err != nil {
			return result
		}
		switch {
		case strings.HasPrefix(line, "EHLO "):
			if text.PrintfLine("250-local fixture\r\n250 AUTH PLAIN") != nil {
				return result
			}
		case strings.HasPrefix(line, "AUTH PLAIN "):
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			if err != nil || string(decoded) != "\x00http-fixture-user\x00HttpFixtureOnly!2026" {
				return result
			}
			result.authenticated = true
			if text.PrintfLine("235 authenticated") != nil {
				return result
			}
		case line == "MAIL FROM:<fixture-sender@example.test>", line == "RCPT TO:<fixture-recipient@example.test>":
			if !result.authenticated || text.PrintfLine("250 accepted envelope") != nil {
				return result
			}
		case line == "DATA":
			if !result.authenticated || text.PrintfLine("354 send message") != nil {
				return result
			}
			result.message, err = text.ReadDotBytes()
			if err != nil || text.PrintfLine("250 accepted") != nil {
				return result
			}
			result.err = nil
			return result
		default:
			return result
		}
	}
}

func writeSMTPHTTPFixtureCertificate(t *testing.T, directory string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ephemeral HTTP SMTP test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, public, private)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, public, private)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string][]byte{
		"ca.pem":     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		"server.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		"server.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
	} {
		if err := os.WriteFile(filepath.Join(directory, name), value, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(directory, "empty-roots"), 0o700); err != nil {
		t.Fatal(err)
	}
}
