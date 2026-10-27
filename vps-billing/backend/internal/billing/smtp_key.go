package billing

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type SMTPCipher struct{ aead cipher.AEAD }

const smtpAAD = "aster:official-plugin:email.smtp:credentials:v1"

func newSMTPCipher(key []byte) (*SMTPCipher, error) {
	if len(key) != 32 {
		return nil, ErrSMTPKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrSMTPKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrSMTPKey
	}
	return &SMTPCipher{aead}, nil
}

// LoadSMTPKey accepts a private, exactly 32-byte raw key. Empty means unconfigured.
// Walk via openat/no-follow so symlinks cannot redirect any path component.
// Root-owned sticky /tmp is permitted; every subsequent directory is still
// checked. No supplied path or OS error is included in the returned error.
func LoadSMTPKey(path string) (*SMTPCipher, error) {
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrSMTPKey
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrSMTPKey
	}
	defer func() { syscall.Close(fd) }()
	for _, part := range parts[:len(parts)-1] {
		next, e := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if e != nil {
			return nil, ErrSMTPKey
		}
		var st syscall.Stat_t
		e = syscall.Fstat(next, &st)
		owned := st.Uid == 0 || st.Uid == uint32(os.Geteuid())
		stickyRoot := st.Uid == 0 && st.Mode&syscall.S_ISVTX != 0
		if e != nil || !owned || (st.Mode&0022 != 0 && !stickyRoot) {
			syscall.Close(next)
			return nil, ErrSMTPKey
		}
		syscall.Close(fd)
		fd = next
	}
	keyfd, err := syscall.Openat(fd, parts[len(parts)-1], syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrSMTPKey
	}
	f := os.NewFile(uintptr(keyfd), "SMTP encryption key")
	defer f.Close()
	var st syscall.Stat_t
	if syscall.Fstat(keyfd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || (st.Mode&07777 != 0400 && st.Mode&07777 != 0600) || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || st.Nlink != 1 || st.Size != 32 {
		return nil, ErrSMTPKey
	}
	key, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil {
		return nil, ErrSMTPKey
	}
	defer clear(key)
	return newSMTPCipher(key)
}

func validSMTPEnvelope(value string) bool {
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "v1:"))
	return strings.HasPrefix(value, "v1:") && err == nil && len(raw) >= 28 && len(raw) <= 3072
}
func (c *SMTPCipher) encrypt(credentials emailCredentials) (string, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(credentials)
	raw := encoded.Bytes()
	if err != nil {
		return "", ErrSMTPKey
	}
	defer clear(raw)
	nonce := make([]byte, c.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", ErrSMTPKey
	}
	sealed := c.aead.Seal(nonce, nonce, raw, []byte(smtpAAD))
	return "v1:" + base64.RawStdEncoding.EncodeToString(sealed), nil
}
func (c *SMTPCipher) decrypt(value string) (emailCredentials, error) {
	if c == nil || !validSMTPEnvelope(value) {
		return emailCredentials{}, ErrSMTPKey
	}
	raw, _ := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "v1:"))
	n := c.aead.NonceSize()
	plain, err := c.aead.Open(nil, raw[:n], raw[n:], []byte(smtpAAD))
	if err != nil {
		return emailCredentials{}, ErrSMTPKey
	}
	defer clear(plain)
	var credentials emailCredentials
	if json.Unmarshal(plain, &credentials) != nil || credentials.Username == "" || credentials.Password == "" || len(credentials.Username) > 254 || len(credentials.Password) > 1024 {
		return emailCredentials{}, ErrSMTPKey
	}
	return credentials, nil
}
func recordEmailAuditFailure(ctx context.Context, actor User, revision int64, outcome string, err error) {
	logger := slog.Default()
	attrs := []any{"outcome", outcome, "revision", revision, "actor_id", actor.ID, "error_class", errorKind(err)}
	if state := observation(ctx); state != nil {
		logger = state.logger
		attrs = append(attrs, "request_id", state.requestID, "route", state.route)
	}
	logger.Log(context.WithoutCancel(ctx), slog.LevelError, "smtp_test_final_audit_failed", attrs...)
}
