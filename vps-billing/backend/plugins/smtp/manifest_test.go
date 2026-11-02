package smtp

import (
	"aster/billing/pkg/pluginapi"
	"strings"
	"testing"
)

func TestSMTPManifestSupportsCurrentFinanceCore(t *testing.T) {
	m := Manifest("1.1.0", strings.Repeat("a", 64))
	if err := m.Validate(pluginapi.CoreVersion); err != nil {
		t.Fatal("shipped SMTP worker cannot serve the current finance core", err)
	}
	if m.Validate("0.7.0") == nil {
		t.Fatal("unreviewed future core accepted")
	}
}
