package pluginapi

import (
	"strings"
	"testing"
)

func TestResourceManifestCannotBorrowPaymentOrLegacyCompatibility(t *testing.T) {
	if CoreVersion != "0.6.0" || ResourceMinCoreVersion != "0.5.10" {
		t.Fatal("resource protocol must begin at its first Core release, 0.5.10")
	}
	m := Manifest{Schema: 1, ID: "resource.aster-cloud", Version: "0.1.0", Protocol: ResourceProtocol, CoreMin: ResourceMinCoreVersion, CoreMaxExclusive: "0.7.0", Capabilities: ResourceCapabilities(), Executable: "plugin", SHA256: strings.Repeat("a", 64)}
	if e := m.Validate(CoreVersion); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"old_core", "old_min", "smtp_id", "payment_capability", "extra_capability"} {
		t.Run(mode, func(t *testing.T) {
			copy := m
			core := CoreVersion
			switch mode {
			case "old_core":
				core = "0.5.9"
			case "old_min":
				copy.CoreMin = "0.5.9"
			case "smtp_id":
				copy.ID = "email.smtp"
			case "payment_capability":
				copy.Capabilities = PaymentCapabilities()
			case "extra_capability":
				copy.Capabilities = append(ResourceCapabilities(), "resource.exec")
			}
			if copy.Validate(core) == nil {
				t.Fatal("invalid capability/minimum accepted")
			}
		})
	}
}

func TestResourceResponseRequiresExplicitCompleteEnvelope(t *testing.T) {
	good := `{"protocol":"aster.resource/1","request_id":"nonce","status":"confirmed","code":"","payload":"e30="}`
	var out ResourceResponse
	if e := DecodeStrict([]byte(good), MaxResponseBytes, &out); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{strings.Replace(good, `,"code":""`, "", 1), strings.Replace(good, `,"payload":"e30="`, "", 1), strings.Replace(good, `"e30="`, `null`, 1), strings.Replace(good, `"status":"confirmed"`, `"status":"confirmed","status":"confirmed"`, 1), strings.Replace(good, `"code":""`, `"code":"","token":"secret-canary"`, 1)} {
		if e := DecodeStrict([]byte(bad), MaxResponseBytes, &out); e == nil || strings.Contains(e.Error(), "canary") {
			t.Fatal("incomplete/secret response accepted", e)
		}
	}
}
