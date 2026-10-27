// Package smtpv1 defines the language-neutral SMTP plugin data contract.
// It contains validation and safe error categories, never an SMTP transport.
package smtpv1

import (
	"context"
	"net"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	STARTTLS    = "starttls"
	ImplicitTLS = "implicit_tls"

	MaxTextBytes    = 64 * 1024
	MaxSubjectBytes = 200

	CodeInvalidConfig     = "SMTP_INVALID_CONFIG"
	CodeInvalidMessage    = "SMTP_INVALID_MESSAGE"
	CodeConnection        = "SMTP_CONNECTION_FAILED"
	CodeTLS               = "SMTP_TLS_FAILED"
	CodeAuth              = "SMTP_AUTH_FAILED"
	CodeRejected          = "SMTP_REJECTED"
	CodeTimeout           = "SMTP_TIMEOUT"
	CodeCanceled          = "SMTP_CANCELED"
	CodeProtocol          = "SMTP_PROTOCOL_ERROR"
	CodeInternal          = "SMTP_INTERNAL_ERROR"
	CodeDeliveryUncertain = "SMTP_DELIVERY_UNCERTAIN"
)

// Config has no insecure transport or certificate-verification bypass. Local
// relays are permitted, but must present a trusted certificate for Host.
type Config struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	TLSMode     string `json:"tls_mode"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	FromAddress string `json:"from_address"`
	FromName    string `json:"from_name"`
}

// Message supports one recipient and a bounded UTF-8 plain-text body only.
type Message struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
}

// Error exposes safe categories only. No underlying SMTP, TLS, DNS or network
// error is retained: those values can contain credentials or provider content.
type Error struct {
	Code                    string
	DeliveryUncertain       bool
	RetryableBeforeDelivery bool
}

func (e *Error) Error() string {
	switch e.Code {
	case CodeInvalidConfig:
		return "SMTP configuration is invalid"
	case CodeInvalidMessage:
		return "SMTP message is invalid"
	case CodeTLS:
		return "SMTP secure connection failed"
	case CodeAuth:
		return "SMTP authentication failed"
	case CodeRejected:
		return "SMTP server rejected the request"
	case CodeTimeout:
		return "SMTP operation timed out"
	case CodeCanceled:
		return "SMTP operation was canceled"
	case CodeDeliveryUncertain:
		return "SMTP delivery outcome is uncertain; do not automatically resend"
	default:
		return "SMTP operation failed"
	}
}

func (e *Error) Is(target error) bool {
	return (e.Code == CodeCanceled && target == context.Canceled) ||
		(e.Code == CodeTimeout && target == context.DeadlineExceeded)
}

func ValidateConfig(cfg Config) error {
	if !validHost(cfg.Host) || cfg.Port < 1 || cfg.Port > 65535 ||
		(cfg.TLSMode != STARTTLS && cfg.TLSMode != ImplicitTLS) ||
		len(cfg.Username) == 0 || len(cfg.Username) > 512 || strings.TrimSpace(cfg.Username) != cfg.Username || !headerText(cfg.Username) ||
		len(cfg.Password) == 0 || len(cfg.Password) > 4096 || !headerText(cfg.Password) ||
		len(cfg.FromName) > 128 || !headerText(cfg.FromName) || ValidateRecipient(cfg.FromAddress) != nil {
		return &Error{Code: CodeInvalidConfig}
	}
	return nil
}

func ValidateRecipient(value string) error {
	if len(value) > 254 || strings.TrimSpace(value) != value || !headerText(value) {
		return &Error{Code: CodeInvalidMessage}
	}
	local, domain, ok := strings.Cut(value, "@")
	if !ok || len(local) == 0 || len(local) > 64 || !dnsName(domain) || strings.Contains(domain, "@") ||
		strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return &Error{Code: CodeInvalidMessage}
	}
	for _, char := range local {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') &&
			!strings.ContainsRune(".!#$%&'*+-/=?^_`{|}~", char) {
			return &Error{Code: CodeInvalidMessage}
		}
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value {
		return &Error{Code: CodeInvalidMessage}
	}
	return nil
}

func ValidateMessage(msg Message) error {
	if ValidateRecipient(msg.To) != nil || len(msg.Subject) > MaxSubjectBytes || strings.TrimSpace(msg.Subject) == "" || !headerText(msg.Subject) ||
		len(msg.Text) == 0 || len(msg.Text) > MaxTextBytes || !utf8.ValidString(msg.Text) {
		return &Error{Code: CodeInvalidMessage}
	}
	for _, char := range msg.Text {
		if unicode.IsControl(char) && char != '\n' && char != '\r' && char != '\t' {
			return &Error{Code: CodeInvalidMessage}
		}
	}
	return nil
}

func headerText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
			return false
		}
	}
	return true
}

func validHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsUnspecified() && !ip.IsMulticast()
	}
	return dnsName(host)
}

func dnsName(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '-' {
				return false
			}
		}
	}
	return true
}
