// Package smtp implements the official SMTP transport. Configuration is supplied
// by the core, in memory, after administrator authorization. It never reads
// credentials from files, logs provider replies, or changes billing state.
package smtp

import (
	"aster/billing/pkg/pluginapi/smtpv1"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"mime"
	"net"
	"net/mail"
	netsmtp "net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

const (
	STARTTLS              = smtpv1.STARTTLS
	ImplicitTLS           = smtpv1.ImplicitTLS
	MaxTextBytes          = smtpv1.MaxTextBytes
	MaxSubjectBytes       = smtpv1.MaxSubjectBytes
	CodeInvalidConfig     = smtpv1.CodeInvalidConfig
	CodeInvalidMessage    = smtpv1.CodeInvalidMessage
	CodeConnection        = smtpv1.CodeConnection
	CodeTLS               = smtpv1.CodeTLS
	CodeAuth              = smtpv1.CodeAuth
	CodeRejected          = smtpv1.CodeRejected
	CodeTimeout           = smtpv1.CodeTimeout
	CodeCanceled          = smtpv1.CodeCanceled
	CodeProtocol          = smtpv1.CodeProtocol
	CodeInternal          = smtpv1.CodeInternal
	CodeDeliveryUncertain = smtpv1.CodeDeliveryUncertain
)

type Config = smtpv1.Config
type Message = smtpv1.Message
type Error = smtpv1.Error

func ValidateConfig(cfg Config) error      { return smtpv1.ValidateConfig(cfg) }
func ValidateRecipient(value string) error { return smtpv1.ValidateRecipient(value) }
func ValidateMessage(msg Message) error    { return smtpv1.ValidateMessage(msg) }

// Internal options let this package test local certificate fixtures without an
// exported production option for weakening TLS or changing certificate roots.
type options struct {
	rootCAs      *x509.CertPool
	dial         func(context.Context, string, string) (net.Conn, error)
	totalTimeout time.Duration
	stageTimeout time.Duration
	dialTimeout  time.Duration
	retryDelay   time.Duration
	replyBudget  int64
}

type Client struct{ options options }

func NewClient() *Client { return &Client{} }

// Send uses verified TLS, a 30-second overall deadline, 10-second stage
// deadlines and a 5-second DNS/connect deadline. At most one retry occurs,
// exclusively before MAIL begins. No connection is reused between attempts.
func Send(ctx context.Context, cfg Config, msg Message) error {
	return NewClient().Send(ctx, cfg, msg)
}

func (c *Client) Send(ctx context.Context, cfg Config, msg Message) error {
	if err := ValidateConfig(cfg); err != nil {
		return err
	}
	if err := ValidateMessage(msg); err != nil {
		return err
	}
	if ctx == nil {
		return &Error{Code: CodeInvalidConfig}
	}
	o := c.options
	if o.totalTimeout <= 0 {
		o.totalTimeout = 30 * time.Second
	}
	if o.stageTimeout <= 0 {
		o.stageTimeout = 10 * time.Second
	}
	if o.dialTimeout <= 0 {
		o.dialTimeout = 5 * time.Second
	}
	if o.retryDelay <= 0 {
		o.retryDelay = 200 * time.Millisecond
	}
	if o.replyBudget <= 0 {
		o.replyBudget = 512 * 1024
	}
	if o.dial == nil {
		dialer := &net.Dialer{Timeout: o.dialTimeout}
		o.dial = dialer.DialContext
	}
	ctx, cancel := context.WithTimeout(ctx, o.totalTimeout)
	defer cancel()
	payload, err := encodeMessage(cfg, msg)
	if err != nil {
		return &Error{Code: CodeInternal}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			return safeError(ctx, ctx.Err(), CodeConnection, false)
		}
		err := sendAttempt(ctx, cfg, msg.To, payload, o)
		if err == nil {
			return nil
		}
		if !err.RetryableBeforeDelivery || attempt == 1 {
			return err
		}
		timer := time.NewTimer(o.retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return safeError(ctx, ctx.Err(), CodeConnection, false)
		case <-timer.C:
		}
	}
	return &Error{Code: CodeInternal}
}

func sendAttempt(ctx context.Context, cfg Config, recipient string, payload []byte, o options) *Error {
	dialContext, cancelDial := context.WithTimeout(ctx, o.dialTimeout)
	raw, err := o.dial(dialContext, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	cancelDial()
	if err != nil {
		return safeError(ctx, err, CodeConnection, true)
	}
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	conn := &boundedConn{Conn: raw, remaining: o.replyBudget}
	stage := func() {
		deadline := time.Now().Add(o.stageTimeout)
		if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
			deadline = end
		}
		_ = conn.SetDeadline(deadline)
	}
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: o.rootCAs}
	var secured net.Conn = conn
	if cfg.TLSMode == ImplicitTLS {
		stage()
		tlsConn := tls.Client(conn, tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return safeError(ctx, err, CodeTLS, false)
		}
		secured = tlsConn
	}
	stage()
	client, err := netsmtp.NewClient(secured, cfg.Host)
	if err != nil {
		return safeError(ctx, err, CodeConnection, true)
	}
	defer client.Close()
	stage()
	if err := client.Hello("localhost"); err != nil {
		return safeError(ctx, err, CodeProtocol, true)
	}
	if cfg.TLSMode == STARTTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return &Error{Code: CodeTLS}
		}
		stage()
		if err := client.StartTLS(tlsConfig); err != nil {
			return safeError(ctx, err, CodeTLS, false)
		}
	}
	state, ok := client.TLSConnectionState()
	if !ok || !state.HandshakeComplete || len(state.VerifiedChains) == 0 {
		return &Error{Code: CodeTLS}
	}
	available, mechanisms := client.Extension("AUTH")
	if !available {
		return &Error{Code: CodeAuth}
	}
	var auth netsmtp.Auth
	for _, mechanism := range strings.Fields(mechanisms) {
		if mechanism == "PLAIN" {
			auth = netsmtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
			break
		}
		if mechanism == "LOGIN" {
			auth = &loginAuth{host: cfg.Host, username: cfg.Username, password: cfg.Password}
		}
	}
	if auth == nil {
		return &Error{Code: CodeAuth}
	}
	stage()
	if err := client.Auth(auth); err != nil {
		return safeError(ctx, err, CodeAuth, true)
	}
	// From this point on no automatic retry is permitted, even for a definite
	// rejection, so only the authorized caller can initiate another send.
	stage()
	if err := client.Mail(cfg.FromAddress); err != nil {
		return safeError(ctx, err, CodeRejected, false)
	}
	stage()
	if err := client.Rcpt(recipient); err != nil {
		return safeError(ctx, err, CodeRejected, false)
	}
	stage()
	id, err := client.Text.Cmd("DATA")
	if err != nil {
		return safeError(ctx, err, CodeProtocol, false)
	}
	client.Text.StartResponse(id)
	_, _, err = client.Text.ReadResponse(354)
	client.Text.EndResponse(id)
	if err != nil {
		return safeError(ctx, err, CodeRejected, false)
	}
	// Use the exported dot writer directly: net/smtp's dataCloser ignores the
	// DotWriter.Close write error. Check both the terminator write and final reply.
	writer := client.Text.DotWriter()
	if _, err := writer.Write(payload); err != nil {
		return &Error{Code: CodeDeliveryUncertain, DeliveryUncertain: true}
	}
	if err := writer.Close(); err != nil {
		return &Error{Code: CodeDeliveryUncertain, DeliveryUncertain: true}
	}
	if _, _, err := client.Text.ReadResponse(250); err != nil {
		var response *textproto.Error
		if errors.As(err, &response) && response.Code >= 400 && response.Code <= 599 {
			return safeError(ctx, err, CodeRejected, false)
		}
		return &Error{Code: CodeDeliveryUncertain, DeliveryUncertain: true}
	}
	// Final 250 means accepted, not delivered to the inbox. Closing/QUIT failures
	// cannot undo acceptance and must not turn this result into a resend request.
	return nil
}

var errReplyBudget = errors.New("SMTP response budget exceeded")

// Bound all peer bytes, including TLS handshake records, before textproto can
// accumulate an unbounded reply line. The independent deadline also bounds time.
type boundedConn struct {
	net.Conn
	remaining int64
}

func (c *boundedConn) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errReplyBudget
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.Conn.Read(p)
	c.remaining -= int64(n)
	return n, err
}

func safeError(ctx context.Context, err error, code string, beforeMail bool) *Error {
	if ctx.Err() == context.Canceled {
		return &Error{Code: CodeCanceled}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return &Error{Code: CodeTimeout}
	}
	if errors.Is(err, errReplyBudget) {
		return &Error{Code: CodeProtocol}
	}
	var response *textproto.Error
	if errors.As(err, &response) {
		return &Error{Code: code, RetryableBeforeDelivery: beforeMail && response.Code >= 400 && response.Code <= 499}
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return &Error{Code: CodeTimeout, RetryableBeforeDelivery: beforeMail}
	}
	return &Error{Code: code, RetryableBeforeDelivery: beforeMail && code == CodeConnection}
}

func encodeMessage(cfg Config, msg Message) ([]byte, error) {
	var identifier [16]byte
	if _, err := rand.Read(identifier[:]); err != nil {
		return nil, err
	}
	_, domain, _ := strings.Cut(cfg.FromAddress, "@")
	var result bytes.Buffer
	result.WriteString("From: " + (&mail.Address{Name: cfg.FromName, Address: cfg.FromAddress}).String() + "\r\n")
	result.WriteString("To: " + (&mail.Address{Address: msg.To}).String() + "\r\n")
	result.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", msg.Subject) + "\r\n")
	result.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	result.WriteString("Message-ID: <" + hex.EncodeToString(identifier[:]) + "@" + domain + ">\r\n")
	result.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	text := strings.ReplaceAll(strings.ReplaceAll(msg.Text, "\r\n", "\n"), "\r", "\n")
	text = strings.ReplaceAll(text, "\n", "\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	for len(encoded) > 76 {
		result.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	result.WriteString(encoded + "\r\n")
	return result.Bytes(), nil
}

type loginAuth struct {
	host, username, password string
	step                     int
}

func (a *loginAuth) Start(info *netsmtp.ServerInfo) (string, []byte, error) {
	if !info.TLS || info.Name != a.host {
		return "", nil, &Error{Code: CodeTLS}
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		if a.step == 2 {
			return nil, nil
		}
		return nil, &Error{Code: CodeAuth}
	}
	a.step++
	switch a.step {
	case 1:
		return []byte(a.username), nil
	case 2:
		return []byte(a.password), nil
	default:
		return nil, &Error{Code: CodeAuth}
	}
}
