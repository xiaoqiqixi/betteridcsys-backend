package resourcev1

import (
	"aster/billing/controllerapi"
	"encoding/json"
	"strings"
	"testing"
)

func request() controllerapi.Request {
	p := controllerapi.ProductSpec{Mode: "preset", ControllerID: "primary", RemoteProductID: "sku-1"}
	digest, _ := p.Digest()
	return controllerapi.Request{Protocol: controllerapi.Protocol, OperationID: strings.Repeat("a", 32), OrderID: strings.Repeat("b", 32), OwnerRef: strings.Repeat("c", 32), Hostname: "web-1", ConfigurationRevision: 2, ConfigurationSHA256: digest, Product: p}
}
func TestExactResourcePayload(t *testing.T) {
	in := request()
	raw, _, _ := in.BytesAndDigest()
	got, e := DecodeRequest(raw)
	if e != nil || !got.Valid() {
		t.Fatal(e)
	}
	for name, bad := range map[string]string{
		"unknown":      strings.Replace(string(raw), `"protocol":`, `"money":5,"protocol":`, 1),
		"duplicate":    strings.Replace(string(raw), `"hostname":"web-1"`, `"hostname":"bad","hostname":"web-1"`, 1),
		"omitted_null": strings.Replace(string(raw), `,"resources":null`, "", 1),
		"case_alias":   strings.Replace(string(raw), `"hostname"`, `"Hostname"`, 1),
		"bad_digest":   strings.Replace(string(raw), `"sku-1"`, `"sku-2"`, 1),
		"trailing":     string(raw) + `{}`, "whitespace": " " + string(raw),
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := DecodeRequest([]byte(bad)); e == nil {
				t.Fatal("accepted ambiguous payload")
			}
		})
	}
	_, hash, _ := in.BytesAndDigest()
	out := controllerapi.Result{Protocol: controllerapi.Protocol, OperationID: in.OperationID, OrderID: in.OrderID, RequestSHA256: hash, Status: "accepted"}
	encoded, _ := json.Marshal(out)
	if _, e := DecodeResult(encoded, in); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{strings.Replace(string(encoded), `,"resource_id":""`, "", 1), strings.Replace(string(encoded), `"accepted"`, `"succeeded"`, 1), strings.Replace(string(encoded), in.OrderID, strings.Repeat("d", 32), 1), strings.Replace(string(encoded), `"status"`, `"Status"`, 1)} {
		if _, e := DecodeResult([]byte(bad), in); e == nil {
			t.Fatal("accepted ambiguous result")
		}
	}
}
