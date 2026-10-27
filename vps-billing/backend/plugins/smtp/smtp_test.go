package smtp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureUser = "fixture-user"
const fixturePassword = "fixture-only-password!"

type serverOptions struct {
	mode              string
	noStartTLS        bool
	mechanisms        string
	greetingFailures  int
	permanentGreeting bool
	mailReply         int
	rcptReply         int
	authReply         int
	finalReply        int
	ambiguous         bool
	stall             string
	hugeGreeting      bool
	hugeEHLO          bool
	beforeData        func()
}

type fixtureServer struct {
	listener net.Listener
	roots    *x509.CertPool
	cert     tls.Certificate
	options  serverOptions
	mu       sync.Mutex
	conns    map[net.Conn]bool
	sessions int
	auths    int
	insecure bool
	commands []string
	messages [][]byte
	wg       sync.WaitGroup
}

func newFixture(t *testing.T, o serverOptions) *fixtureServer {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ephemeral SMTP fixture CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, public, private)
	if err != nil {
		t.Fatal(err)
	}
	parsedCA, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"relay.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, parsedCA, public, private)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if o.mode == "" {
		o.mode = STARTTLS
	}
	if o.mechanisms == "" {
		o.mechanisms = "PLAIN LOGIN"
	}
	server := &fixtureServer{listener: listener, roots: x509.NewCertPool(), cert: tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: private}, options: o, conns: map[net.Conn]bool{}}
	server.roots.AddCert(parsedCA)
	server.wg.Add(1)
	go func() {
		defer server.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.mu.Lock()
			server.sessions++
			number := server.sessions
			server.conns[conn] = true
			server.mu.Unlock()
			server.wg.Add(1)
			go func() {
				defer server.wg.Done()
				defer conn.Close()
				defer func() { server.mu.Lock(); delete(server.conns, conn); server.mu.Unlock() }()
				server.session(conn, number)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		server.mu.Lock()
		for conn := range server.conns {
			_ = conn.Close()
		}
		server.mu.Unlock()
		server.wg.Wait()
	})
	return server
}

func (s *fixtureServer) session(raw net.Conn, number int) {
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	var conn net.Conn = raw
	secure := false
	upgrade := func() bool {
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12})
		if err := tlsConn.Handshake(); err != nil {
			return false
		}
		conn = tlsConn
		secure = true
		return true
	}
	if s.options.mode == ImplicitTLS && !upgrade() {
		return
	}
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	reply := func(value string) bool {
		_, err := writer.WriteString(value)
		return err == nil && writer.Flush() == nil
	}
	stall := func() { _, _ = io.Copy(io.Discard, reader) }
	if s.options.stall == "greeting" {
		stall()
		return
	}
	if s.options.hugeGreeting {
		reply("220 " + strings.Repeat("x", 32*1024) + "\r\n")
		return
	}
	if s.options.permanentGreeting {
		reply("550 fixture-private-provider-reply " + fixturePassword + "\r\n")
		return
	}
	if number <= s.options.greetingFailures {
		reply("421 temporary fixture greeting\r\n")
		return
	}
	if !reply("220 relay.test SMTP fixture\r\n") {
		return
	}
	authed := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		verb, _, _ := strings.Cut(line, " ")
		s.mu.Lock()
		s.commands = append(s.commands, verb)
		s.mu.Unlock()
		switch verb {
		case "EHLO":
			if s.options.hugeEHLO {
				reply(strings.Repeat("250-long multiline fixture response\r\n", 2000) + "250 complete\r\n")
				return
			}
			if s.options.stall == "ehlo" {
				stall()
				return
			}
			response := "250-relay.test\r\n"
			if !secure && !s.options.noStartTLS {
				response += "250-STARTTLS\r\n"
			}
			response += "250-AUTH " + s.options.mechanisms + "\r\n250 SIZE 100000\r\n"
			if !reply(response) {
				return
			}
		case "HELO":
			if !reply("250 relay.test\r\n") {
				return
			}
		case "STARTTLS":
			if !reply("220 begin verified TLS\r\n") {
				return
			}
			if s.options.stall == "tls" {
				stall()
				return
			}
			if !upgrade() {
				return
			}
			reader, writer = bufio.NewReader(conn), bufio.NewWriter(conn)
		case "AUTH":
			s.mu.Lock()
			s.auths++
			if !secure {
				s.insecure = true
			}
			s.mu.Unlock()
			if s.options.authReply != 0 {
				reply(strconv.Itoa(s.options.authReply) + " fixture-private-provider-reply\r\n")
				continue
			}
			if strings.HasPrefix(line, "AUTH PLAIN ") {
				payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
				if err != nil || string(payload) != "\x00"+fixtureUser+"\x00"+fixturePassword {
					reply("535 invalid fixture credentials\r\n")
					continue
				}
			} else if line == "AUTH LOGIN" {
				if !reply("334 VXNlcm5hbWU6\r\n") {
					return
				}
				user, err := reader.ReadString('\n')
				if err != nil || strings.TrimSpace(user) != base64.StdEncoding.EncodeToString([]byte(fixtureUser)) || !reply("334 UGFzc3dvcmQ6\r\n") {
					return
				}
				password, err := reader.ReadString('\n')
				if err != nil || strings.TrimSpace(password) != base64.StdEncoding.EncodeToString([]byte(fixturePassword)) {
					return
				}
			} else {
				reply("504 unsupported fixture mechanism\r\n")
				continue
			}
			authed = true
			if !reply("235 authenticated\r\n") {
				return
			}
		case "MAIL", "RCPT":
			if !secure || !authed {
				reply("530 TLS/auth required\r\n")
				continue
			}
			code := s.options.mailReply
			if verb == "RCPT" {
				code = s.options.rcptReply
			}
			if code == 0 {
				code = 250
			}
			if !reply(strconv.Itoa(code) + " fixture-private-provider-reply\r\n") {
				return
			}
		case "DATA":
			if s.options.beforeData != nil {
				s.options.beforeData()
			}
			if !reply("354 send bounded message\r\n") {
				return
			}
			text := textproto.NewReader(reader)
			body, err := text.ReadDotBytes()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.messages = append(s.messages, body)
			s.mu.Unlock()
			if s.options.ambiguous {
				return
			}
			if s.options.stall == "final" {
				stall()
				return
			}
			code := s.options.finalReply
			if code == 0 {
				code = 250
			}
			reply(strconv.Itoa(code) + " fixture-private-provider-reply\r\n")
			return // Deliberately close immediately after the final reply.
		case "*":
			reply("501 canceled authentication\r\n")
		case "QUIT":
			reply("221 closing\r\n")
			return
		default:
			reply("500 unsupported fixture command\r\n")
		}
	}
}

func (s *fixtureServer) client(t *testing.T) (*Client, Config) {
	t.Helper()
	_, port, _ := net.SplitHostPort(s.listener.Addr().String())
	number, _ := strconv.Atoi(port)
	cfg := Config{Host: "relay.test", Port: number, TLSMode: s.options.mode, Username: fixtureUser, Password: fixturePassword, FromAddress: "sender@example.test", FromName: "星屿云"}
	client := &Client{options: options{rootCAs: s.roots, totalTimeout: 2 * time.Second, stageTimeout: 500 * time.Millisecond, dialTimeout: 200 * time.Millisecond, retryDelay: time.Millisecond,
		dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, s.listener.Addr().String())
		}}}
	return client, cfg
}

func testMessage() Message {
	return Message{To: "recipient@example.test", Subject: "邮件测试 / SMTP test", Text: "你好，世界。\n.literal-dot\r\nThird line\rFourth"}
}

func smtpError(t *testing.T, err error, code string, uncertain bool) *Error {
	t.Helper()
	var result *Error
	if !errors.As(err, &result) || result.Code != code || result.DeliveryUncertain != uncertain {
		t.Fatalf("unexpected safe result: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%+v", err), fixturePassword) || strings.Contains(err.Error(), "fixture-private-provider-reply") || errors.Unwrap(err) != nil {
		t.Fatal("provider content/cause escaped transport")
	}
	return result
}

func TestVerifiedTLSModesAndAuthMechanisms(t *testing.T) {
	for _, mode := range []string{STARTTLS, ImplicitTLS} {
		for _, mechanism := range []string{"PLAIN", "LOGIN", "LOGIN PLAIN"} {
			t.Run(mode+"/"+mechanism, func(t *testing.T) {
				server := newFixture(t, serverOptions{mode: mode, mechanisms: mechanism})
				client, cfg := server.client(t)
				if err := client.Send(context.Background(), cfg, testMessage()); err != nil {
					t.Fatal(err)
				}
				server.mu.Lock()
				defer server.mu.Unlock()
				if server.sessions != 1 || server.auths != 1 || server.insecure || len(server.messages) != 1 {
					t.Fatal("TLS/auth/delivery fixture counts mismatch")
				}
				message, err := mail.ReadMessage(bytes.NewReader(server.messages[0]))
				if err != nil {
					t.Fatal(err)
				}
				subject, err := (&mime.WordDecoder{}).DecodeHeader(message.Header.Get("Subject"))
				if err != nil || subject != testMessage().Subject {
					t.Fatal("subject was not valid encoded UTF-8")
				}
				from, err := mail.ParseAddress(message.Header.Get("From"))
				if err != nil || from.Name != cfg.FromName || from.Address != cfg.FromAddress {
					t.Fatal("sender was not preserved")
				}
				decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
				if err != nil || string(decoded) != "你好，世界。\r\n.literal-dot\r\nThird line\r\nFourth" {
					t.Fatal("MIME text/line endings changed unexpectedly")
				}
				if message.Header.Get("Date") == "" || message.Header.Get("Message-ID") == "" || len(message.Header["To"]) != 1 {
					t.Fatal("required unique headers missing")
				}
			})
		}
	}
}

func TestTLSFailureNeverAuthenticatesOrFallsBack(t *testing.T) {
	for _, mode := range []string{STARTTLS, ImplicitTLS} {
		for _, failure := range []string{"untrusted_ca", "wrong_hostname"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				server := newFixture(t, serverOptions{mode: mode})
				client, cfg := server.client(t)
				if failure == "untrusted_ca" {
					client.options.rootCAs = x509.NewCertPool()
				} else {
					cfg.Host = "wrong.test"
				}
				smtpError(t, client.Send(context.Background(), cfg, testMessage()), CodeTLS, false)
				server.mu.Lock()
				defer server.mu.Unlock()
				if server.sessions != 1 || server.auths != 0 || len(server.messages) != 0 {
					t.Fatal("TLS failure retried or reached credentials/data")
				}
			})
		}
	}
}

func TestMissingSTARTTLSNeverSendsCredentials(t *testing.T) {
	server := newFixture(t, serverOptions{noStartTLS: true})
	client, cfg := server.client(t)
	smtpError(t, client.Send(context.Background(), cfg, testMessage()), CodeTLS, false)
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.auths != 0 || server.sessions != 1 || len(server.messages) != 0 {
		t.Fatal("STARTTLS was downgraded")
	}
}

func TestTransientGreetingRetriesOnlyBeforeMail(t *testing.T) {
	server := newFixture(t, serverOptions{greetingFailures: 1})
	client, cfg := server.client(t)
	if err := client.Send(context.Background(), cfg, testMessage()); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.sessions != 2 || len(server.messages) != 1 || server.auths != 1 {
		t.Fatal("safe pre-transaction retry count mismatch")
	}
}

func TestTransientRetryBound(t *testing.T) {
	server := newFixture(t, serverOptions{greetingFailures: 10})
	client, cfg := server.client(t)
	err := smtpError(t, client.Send(context.Background(), cfg, testMessage()), CodeConnection, false)
	if !err.RetryableBeforeDelivery {
		t.Fatal("definite pre-delivery transient classification lost")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.sessions != 2 || server.auths != 0 || len(server.messages) != 0 {
		t.Fatal("retry was not bounded to two attempts")
	}
}

func TestPermanentRejectionsNeverRetryOrLeakProviderData(t *testing.T) {
	for _, test := range []struct {
		name    string
		options serverOptions
		code    string
	}{
		{"greeting", serverOptions{permanentGreeting: true}, CodeConnection},
		{"auth", serverOptions{authReply: 535}, CodeAuth},
		{"mail", serverOptions{mailReply: 550}, CodeRejected},
		{"recipient", serverOptions{rcptReply: 550}, CodeRejected},
		{"mail_transient_after_transaction", serverOptions{mailReply: 451}, CodeRejected},
		{"final_negative", serverOptions{finalReply: 451}, CodeRejected},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newFixture(t, test.options)
			client, cfg := server.client(t)
			err := smtpError(t, client.Send(context.Background(), cfg, testMessage()), test.code, false)
			if err.RetryableBeforeDelivery {
				t.Fatal("unsafe replay suggested")
			}
			server.mu.Lock()
			defer server.mu.Unlock()
			if server.sessions != 1 {
				t.Fatal("definitive rejection retried")
			}
		})
	}
}

func TestAmbiguousDATAIsNeverReplayed(t *testing.T) {
	for _, o := range []serverOptions{{ambiguous: true}, {stall: "final"}} {
		server := newFixture(t, o)
		client, cfg := server.client(t)
		client.options.stageTimeout = 50 * time.Millisecond
		err := smtpError(t, client.Send(context.Background(), cfg, testMessage()), CodeDeliveryUncertain, true)
		if err.RetryableBeforeDelivery {
			t.Fatal("uncertain delivery marked retryable")
		}
		server.mu.Lock()
		if server.sessions != 1 || len(server.messages) != 1 {
			t.Fatal("uncertain message was replayed")
		}
		server.mu.Unlock()
	}
}

func TestStageAndTotalTimeouts(t *testing.T) {
	for _, stage := range []string{"greeting", "ehlo", "tls"} {
		t.Run(stage, func(t *testing.T) {
			server := newFixture(t, serverOptions{stall: stage})
			client, cfg := server.client(t)
			client.options.stageTimeout = 40 * time.Millisecond
			client.options.totalTimeout = 120 * time.Millisecond
			start := time.Now()
			smtpError(t, client.Send(context.Background(), cfg, testMessage()), CodeTimeout, false)
			if time.Since(start) > time.Second {
				t.Fatal("stage/overall deadline did not bound work")
			}
		})
	}
}

func TestContextCancellationClosesActiveSession(t *testing.T) {
	server := newFixture(t, serverOptions{stall: "greeting"})
	client, cfg := server.client(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	start := time.Now()
	err := client.Send(ctx, cfg, testMessage())
	smtpError(t, err, CodeCanceled, false)
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatal("cancellation was not prompt")
	}
}

func TestDialContextIsBoundedAndRetryLimitApplies(t *testing.T) {
	server := newFixture(t, serverOptions{})
	client, cfg := server.client(t)
	client.options.dialTimeout = 15 * time.Millisecond
	attempts := 0
	client.options.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		attempts++
		<-ctx.Done()
		return nil, ctx.Err()
	}
	smtpError(t, client.Send(context.Background(), cfg, testMessage()), CodeTimeout, false)
	if attempts != 2 {
		t.Fatal("dial attempts not bounded")
	}
}

func TestReplyByteBudgetRejectsUnboundedBanner(t *testing.T) {
	server := newFixture(t, serverOptions{hugeGreeting: true})
	client, cfg := server.client(t)
	client.options.replyBudget = 4096
	smtpError(t, client.Send(context.Background(), cfg, testMessage()), CodeProtocol, false)
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.sessions != 1 || server.auths != 0 {
		t.Fatal("oversized peer reply retried or authenticated")
	}
}

func TestReplyByteBudgetRejectsUnboundedMultilineEHLO(t *testing.T) {
	server := newFixture(t, serverOptions{hugeEHLO: true})
	client, cfg := server.client(t)
	client.options.replyBudget = 4096
	smtpError(t, client.Send(context.Background(), cfg, testMessage()), CodeProtocol, false)
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.sessions != 1 || server.auths != 0 {
		t.Fatal("oversized multiline reply retried or authenticated")
	}
}

type failDataWriteConn struct {
	net.Conn
	fail *atomic.Bool
}

func (c *failDataWriteConn) Write(data []byte) (int, error) {
	if c.fail.Load() {
		return 0, io.ErrUnexpectedEOF
	}
	return c.Conn.Write(data)
}

func TestFailedDATATerminatorFlushIsUncertainAndNeverRetried(t *testing.T) {
	var fail atomic.Bool
	server := newFixture(t, serverOptions{beforeData: func() { fail.Store(true) }})
	client, cfg := server.client(t)
	dial := client.options.dial
	client.options.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &failDataWriteConn{Conn: conn, fail: &fail}, nil
	}
	// This small message remains buffered until DotWriter.Close flushes DATA.
	err := smtpError(t, client.Send(context.Background(), cfg, testMessage()), CodeDeliveryUncertain, true)
	if err.RetryableBeforeDelivery {
		t.Fatal("failed DATA flush suggested replay")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.sessions != 1 || server.auths != 1 || len(server.messages) != 0 {
		t.Fatal("failed DATA flush was retried or counted accepted")
	}
}

func TestHeaderEnvelopeAndSizeValidationBeforeDial(t *testing.T) {
	server := newFixture(t, serverOptions{})
	client, cfg := server.client(t)
	for _, bad := range []string{"Name <recipient@example.test>", "one@example.test,two@example.test", "recipient@example.test\r\nBcc: another@example.test", "a..b@example.test", "\"quoted\"@example.test", "用户@example.test", "user@[127.0.0.1]", "user@-invalid.test", strings.Repeat("a", 65) + "@example.test"} {
		msg := testMessage()
		msg.To = bad
		smtpError(t, client.Send(context.Background(), cfg, msg), CodeInvalidMessage, false)
	}
	for _, subject := range []string{"", "valid\nBcc: bad", "hidden\u202e", strings.Repeat("a", MaxSubjectBytes+1)} {
		msg := testMessage()
		msg.Subject = subject
		smtpError(t, client.Send(context.Background(), cfg, msg), CodeInvalidMessage, false)
	}
	for _, body := range []string{"", "nul\x00", "invalid\xff", strings.Repeat("x", MaxTextBytes+1)} {
		msg := testMessage()
		msg.Text = body
		smtpError(t, client.Send(context.Background(), cfg, msg), CodeInvalidMessage, false)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.sessions != 0 {
		t.Fatal("invalid input reached network")
	}
}

func TestConfigValidationAndLocalRelaySupport(t *testing.T) {
	base := Config{Host: "localhost", Port: 587, TLSMode: STARTTLS, Username: fixtureUser, Password: fixturePassword, FromAddress: "sender@example.test", FromName: "示例服务"}
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "192.168.1.1", "mail.internal", "relay.example.test"} {
		cfg := base
		cfg.Host = host
		if err := ValidateConfig(cfg); err != nil {
			t.Fatalf("operator local relay rejected: %v", err)
		}
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Host = "https://relay.test" }, func(c *Config) { c.Host = "host\r\nAUTH" },
		func(c *Config) { c.Host = "[::1]" }, func(c *Config) { c.Host = "host:587" },
		func(c *Config) { c.Host = "fe80::1%eth0" }, func(c *Config) { c.Host = "0.0.0.0" },
		func(c *Config) { c.Port = 0 }, func(c *Config) { c.Port = 65536 },
		func(c *Config) { c.TLSMode = "plain" }, func(c *Config) { c.Username = "" },
		func(c *Config) { c.Password = "" }, func(c *Config) { c.Password = "secret\x00delimiter" },
		func(c *Config) { c.FromName = "injected\r\nBcc: other@example.test" },
		func(c *Config) { c.FromAddress = "sender@example.test,other@example.test" },
	} {
		cfg := base
		change(&cfg)
		smtpError(t, ValidateConfig(cfg), CodeInvalidConfig, false)
	}
}

func TestConcurrentSendsUseIndependentSessions(t *testing.T) {
	server := newFixture(t, serverOptions{})
	client, cfg := server.client(t)
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.Send(context.Background(), cfg, testMessage()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.sessions != 5 || len(server.messages) != 5 || server.auths != 5 || server.insecure {
		t.Fatal("concurrent messages shared session state")
	}
}
