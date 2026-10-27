package updates

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReleaseTokenOpaqueBearerValues(t *testing.T) {
	for name, value := range map[string]string{
		"minimum":           "a",
		"complete_alphabet": "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~+/==",
		"legacy_shaped":     "ghp_synthetic_opaque_0123456789",
		"opaque_segments":   "a.b.c",
		// Synthetic dots/segments are not decoded or checked for JWT semantics.
		"long_installation_shaped": "ghs_12345_" + strings.Repeat("Ab9_-", 102) + ".opaque",
		"maximum":                  strings.Repeat("a", 4096),
		"trailing_padding":         "a" + strings.Repeat("=", 4095),
	} {
		t.Run(name, func(t *testing.T) {
			for _, ending := range []string{"", "\n", "\r\n"} {
				got, err := releaseTokenValue([]byte(value + ending))
				if err != nil || got != value {
					t.Fatal("valid opaque bearer rejected or altered")
				}
			}
		})
	}
}

func TestReleaseTokenRejectsUnsafeOrOversizeValues(t *testing.T) {
	for name, value := range map[string]string{
		"empty": "", "lf_only": "\n", "crlf_only": "\r\n", "lone_cr": "a\r",
		"embedded_cr": "a\rb", "embedded_lf": "a\nb", "header_injection": "a\r\nAuthorization: injected",
		"repeated_lf": "a\n\n", "repeated_crlf": "a\r\n\r\n", "space": "a b", "tab": "a\tb", "leading_space": " a", "trailing_space": "a ",
		"mixed_lfcr": "a\n\r", "mixed_crlf_lf": "a\r\n\n",
		"non_ascii": "aé", "invalid_utf8": string([]byte{'a', 0xff}), "nul": "a\x00b", "del": "a\x7f",
		"leading_padding": "=a", "padding_only": "=", "embedded_padding": "a=b", "padding_then_punctuation": "a=_",
		"colon": "a:b", "semicolon": "a;b", "quote": "a\"b", "backslash": "a\\b", "comma": "a,b", "brackets": "a[b]",
		"oversize_value": strings.Repeat("a", 4097), "oversize_value_lf": strings.Repeat("a", 4097) + "\n", "oversize_value_crlf": strings.Repeat("a", 4097) + "\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := releaseTokenValue([]byte(value))
			if !errors.Is(err, ErrUnavailable) || got != "" {
				t.Fatal("unsafe bearer accepted or failure exposed payload")
			}
			if err.Error() != ErrUnavailable.Error() {
				t.Fatal("credential error is not redacted")
			}
		})
	}
}

func TestProtectedReleaseTokenMaximumFileAndPermissions(t *testing.T) {
	root, err := os.Stat("/")
	if err != nil {
		t.Fatal(err)
	}
	owner := root.Sys().(*syscall.Stat_t).Uid
	if owner != 0 && owner != uint32(os.Geteuid()) {
		t.Skip("filesystem root is outside the protected loader ownership boundary")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "release-token")
	value := strings.Repeat("a", 4096)
	if err := os.WriteFile(path, []byte(value+"\r\n"), 0400); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadTokenFile(path); err != nil || got != value {
		t.Fatal("protected maximum value with CRLF rejected")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadTokenFile(path); err == nil || got != "" {
		t.Fatal("writable credential accepted")
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked-token")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadTokenFile(link); err == nil || got != "" {
		t.Fatal("symlink credential accepted")
	}
	hardlink := filepath.Join(dir, "hardlinked-token")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadTokenFile(path); err == nil || got != "" {
		t.Fatal("multiply-linked credential accepted")
	}
}

func TestLongOpaqueBearerIsForwardedUnchanged(t *testing.T) {
	value := "ghs_123456_" + strings.Repeat("aZ9-._~+/", 60) + "=="
	token, err := releaseTokenValue([]byte(value + "\r\n"))
	if err != nil {
		t.Fatal("synthetic long bearer rejected")
	}
	client := newGitHubClient(token)
	called := false
	client.http.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		called = true
		if req.Header.Get("Authorization") != "Bearer "+value {
			t.Fatal("opaque bearer changed before request")
		}
		if req.URL.Scheme != "https" || req.URL.Host != "api.github.com" {
			t.Fatal("official request target changed")
		}
		return response(http.StatusOK, []byte(`{}`)), nil
	})
	if _, err := client.fetch(context.Background(), "/releases", false, 64); err != nil || !called {
		t.Fatal("mock official request failed", err)
	}
}
