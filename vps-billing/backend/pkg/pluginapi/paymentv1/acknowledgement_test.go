package paymentv1

import (
	"strings"
	"testing"
	"time"
)

func TestCallbackAcknowledgementBounds(t *testing.T) {
	for _, ack := range []*CallbackAcknowledgement{
		nil,
		{StatusCode: 204},
		{StatusCode: 200, ContentType: "text/plain", Body: "success"},
		{StatusCode: 200, ContentType: "text/plain", Body: strings.Repeat("a", 4096)},
		{StatusCode: 200, ContentType: "application/json", Body: `{"code":"SUCCESS","message":"成功"}`},
		{StatusCode: 200, ContentType: "application/xml", Body: `<?xml version="1.0"?><xml><return_code><![CDATA[SUCCESS]]></return_code></xml>`},
		{StatusCode: 200, ContentType: "text/xml", Body: `<xml><sign>fixture-signature</sign></xml>`},
	} {
		if ack.Validate() != nil {
			t.Fatalf("valid acknowledgement rejected: %#v", ack)
		}
	}
	for name, ack := range map[string]CallbackAcknowledgement{
		"omitted status":        {ContentType: "text/plain", Body: "success"},
		"redirect":              {StatusCode: 302, ContentType: "text/plain", Body: "success"},
		"error override":        {StatusCode: 503, ContentType: "text/plain", Body: "success"},
		"204 with body":         {StatusCode: 204, Body: "success"},
		"204 with type":         {StatusCode: 204, ContentType: "text/plain"},
		"html":                  {StatusCode: 200, ContentType: "text/html", Body: "<script>alert(1)</script>"},
		"injected header":       {StatusCode: 200, ContentType: "text/plain\r\nSet-Cookie: x=y"},
		"oversized":             {StatusCode: 200, ContentType: "text/plain", Body: strings.Repeat("a", 4097)},
		"utf8 byte limit":       {StatusCode: 200, ContentType: "text/plain", Body: strings.Repeat("成", 1366)},
		"invalid utf8":          {StatusCode: 200, ContentType: "text/plain", Body: string([]byte{0xff})},
		"nul":                   {StatusCode: 200, ContentType: "text/plain", Body: "success\x00"},
		"invalid json":          {StatusCode: 200, ContentType: "application/json", Body: `{"ok":true} {}`},
		"empty xml":             {StatusCode: 200, ContentType: "application/xml"},
		"unclosed xml":          {StatusCode: 200, ContentType: "application/xml", Body: `<xml>`},
		"multiple roots":        {StatusCode: 200, ContentType: "application/xml", Body: `<xml/><xml/>`},
		"trailing text":         {StatusCode: 200, ContentType: "application/xml", Body: `<xml/>success`},
		"dtd":                   {StatusCode: 200, ContentType: "application/xml", Body: `<!DOCTYPE xml [<!ENTITY x SYSTEM "file:///etc/passwd">]><xml>&x;</xml>`},
		"stylesheet":            {StatusCode: 200, ContentType: "application/xml", Body: `<?xml-stylesheet href="https://evil.test"?><xml/>`},
		"nested instructions":   {StatusCode: 200, ContentType: "application/xml", Body: `<xml><?xml version="1.0"?></xml>`},
		"excessive xml nesting": {StatusCode: 200, ContentType: "application/xml", Body: strings.Repeat("<x>", 17) + strings.Repeat("</x>", 17)},
	} {
		t.Run(name, func(t *testing.T) {
			if ack.Validate() == nil {
				t.Fatal("invalid acknowledgement accepted")
			}
		})
	}
}

func TestVerifiedEventRejectsUnsafeAcknowledgement(t *testing.T) {
	now := time.Now()
	d := Declarations{Currencies: []Currency{{"USD", 2}}}
	e := VerifiedEvent{AttemptID: "attempt", ProviderID: "payment.example", EventID: "event", TransactionID: "transaction", MerchantAccountID: "merchant", AccountID: "account", OrderID: "order", InvoiceID: "invoice", AmountMinor: 100, Currency: "USD", CurrencyDecimalPlaces: 2, SettledAt: now, Status: "settled"}
	if ValidateEvent(e, e.ProviderID, e.MerchantAccountID, d, now) != nil {
		t.Fatal("complete evidence rejected")
	}
	e.Acknowledgement = &CallbackAcknowledgement{StatusCode: 200, ContentType: "text/html", Body: "<script/>"}
	if ValidateEvent(e, e.ProviderID, e.MerchantAccountID, d, now) == nil {
		t.Fatal("verified event bypassed acknowledgement validation")
	}
}
