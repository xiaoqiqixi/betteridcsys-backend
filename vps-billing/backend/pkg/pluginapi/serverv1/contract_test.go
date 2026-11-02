package serverv1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMutationDigestAndEphemeralSecrets(t *testing.T) {
	r := Request{Protocol: Protocol, Method: "create", OperationID: "op", ServiceID: "service", OwnerRef: "owner", Secrets: Fields{"password": "only-in-memory-fixture"}}
	if r.Seal() != nil || r.Verify() != nil {
		t.Fatal("valid request rejected")
	}
	raw, _ := json.Marshal(r.Redacted())
	if strings.Contains(string(raw), "only-in-memory") {
		t.Fatal("secret persisted")
	}
	r.Secrets["password"] = "changed"
	if r.Verify() == nil {
		t.Fatal("changed request digest accepted")
	}
}
func TestDecodeRejectsDuplicateAndTrailingFields(t *testing.T) {
	for _, raw := range []string{`{"protocol":"aster.server/1","method":"describe","method":"terminate"}`, `{"protocol":"aster.server/1","method":"describe"} {}`} {
		var r Request
		if Decode([]byte(raw), &r) == nil {
			t.Fatal("ambiguous request accepted")
		}
	}
}
func TestSchemaValidationAndNormalization(t *testing.T) {
	min := float64(1)
	s := Schema{Type: "object", Properties: map[string]Field{"count": {Type: "integer", Minimum: &min, Default: float64(2)}, "enabled": {Type: "boolean"}, "password": {Type: "string", WriteOnly: true}}, Required: []string{"enabled"}}
	out, d, e := s.Normalize(Fields{"enabled": false, "password": "private"})
	if e != nil || !ValidDigest(d) || out["count"] != float64(2) {
		t.Fatal(out, e)
	}
	public, secret := s.Split(out)
	if public["password"] != nil || secret["password"] != "private" {
		t.Fatal("secret split")
	}
	for _, in := range []Fields{{"enabled": false, "count": 1.5}, {"enabled": false, "extra": true}, {"enabled": "false"}, {}} {
		if _, _, e = s.Normalize(in); e == nil {
			t.Fatal("invalid input accepted")
		}
	}
}
