package serviceapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testOwner = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestEveryActionCanonicalContract(t *testing.T) {
	actions := map[string]Params{
		"start": {}, "stop": {}, "reboot": {}, "force_off": {}, "suspend": {}, "unsuspend": {StartAfter: true}, "terminate": {Confirmation: testID},
		"reinstall": {ImageID: "ubuntu-24", SSHPublicKey: "ssh-ed25519 AAAA"}, "reset_password": {Password: "temporary <secret>"}, "rescue_enter": {ImageID: "rescue-1"}, "rescue_exit": {},
		"snapshot_create": {Name: "before-update"}, "snapshot_restore": {SnapshotID: testID}, "snapshot_delete": {SnapshotID: testID}, "resize": {CPUCores: 2, MemoryMB: 1024, DiskGB: 40},
	}
	for action, p := range actions {
		t.Run(action, func(t *testing.T) {
			r := ActionRequest{Protocol, testID, testOwner, action, p}
			raw, h, e := r.BytesAndDigest()
			if e != nil {
				t.Fatal(e)
			}
			sum := sha256.Sum256(raw)
			if hex.EncodeToString(sum[:]) != h {
				t.Fatal("digest mismatch")
			}
			var round ActionRequest
			if Decode(raw, &round) != nil || !round.Params.Equal(r.Params) || round.Protocol != r.Protocol || round.OperationID != r.OperationID || round.OwnerRef != r.OwnerRef || round.Action != r.Action {
				t.Fatal("roundtrip")
			}
			again, h2, e := round.BytesAndDigest()
			if e != nil || string(raw) != string(again) || h != h2 {
				t.Fatal("noncanonical")
			}
			r.Params.Name = "unexpected"
			if action != "snapshot_create" && r.Valid() {
				t.Fatal("cross-action parameter accepted")
			}
		})
	}
	r := ActionRequest{Protocol, testID, testOwner, "start", Params{}}
	raw, _, _ := r.BytesAndDigest()
	want := `{"protocol":"aster.service/1","operation_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","owner_ref":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","action":"start","params":{}}`
	if string(raw) != want {
		t.Fatalf("wire changed: %s", raw)
	}
	// {ImageID only}: no credential. {Password+PasswordHash}: two forms of the
	// same secret. {newline password}: malformed. {bad resize}. None is valid.
	for _, p := range []Params{{ImageID: "ubuntu"}, {ImageID: "ubuntu", Password: "p", PasswordHash: "x"}, {Password: "p\ncommand"}, {CPUCores: 1, MemoryMB: 129, DiskGB: 4}} {
		if p.Valid("reinstall") || p.Valid("reset_password") || p.Valid("resize") {
			t.Fatal("invalid union accepted")
		}
	}
	// Mixing the single-key and multi-key forms is rejected.
	if (Params{ImageID: "ubuntu", SSHPublicKey: "ssh-ed25519 AAAA", SSHPublicKeys: []string{"ssh-rsa BBBB"}}).Valid("reinstall") {
		t.Fatal("mixed key forms accepted")
	}
	// Eleven keys and duplicate keys are both rejected.
	var many []string
	for i := 0; i < 11; i++ {
		many = append(many, "ssh-ed25519 KEY"+string(rune('A'+i)))
	}
	if (Params{ImageID: "ubuntu", SSHPublicKeys: many}).Valid("reinstall") {
		t.Fatal("eleven keys accepted")
	}
	if (Params{ImageID: "ubuntu", SSHPublicKeys: []string{"ssh-ed25519 DUP", "ssh-ed25519 DUP"}}).Valid("reinstall") {
		t.Fatal("duplicate keys accepted")
	}
	// Valid unions: password + multiple keys for reinstall; keys-only for reset.
	if !(Params{ImageID: "ubuntu", Password: "secret-123456", SSHPublicKeys: []string{"ssh-ed25519 AAAA", "ssh-rsa BBBB"}}).Valid("reinstall") {
		t.Fatal("password plus keys rejected")
	}
	if !(Params{SSHPublicKeys: []string{"ssh-ed25519 AAAA"}}).Valid("reset_password") {
		t.Fatal("keys-only reset rejected")
	}
	if (Params{Password: "secret"}).Redacted().Password != "" {
		t.Fatal("redaction")
	}
}
func TestEveryResponseWireContract(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		value any
		want  string
	}{
		{ActionResult{testID, "accepted", "ACCEPTED"}, `{"operation_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"accepted","code":"ACCEPTED"}`},
		{Operation{testID, "start", "running", "RUNNING", nil}, `{"operation_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","action":"start","status":"running","code":"RUNNING","finished_at":null}`},
		{[]Image{{"ubuntu", "linux", "Ubuntu", "24.04", 8, strings.Repeat("a", 64)}}, `[{"id":"ubuntu","family":"linux","name":"Ubuntu","version":"24.04","min_disk_gb":8,"sha256":"` + strings.Repeat("a", 64) + `"}]`},
		{Service{Protocol: Protocol, ResourceID: testID, OwnerRef: testOwner, Power: "unknown", IPv4: []string{}, IPv6: []string{}, OSImageID: "ubuntu", Specs: Specs{1, 512, 8, 0}, Traffic: Traffic{PeriodStart: at, PeriodEnd: at}}, `{"protocol":"aster.service/1","resource_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","owner_ref":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","power":"unknown","suspended":false,"rescue_active":false,"ipv4":[],"ipv6":[],"os_image_id":"ubuntu","specs":{"cpu_cores":1,"memory_mb":512,"disk_gb":8,"bandwidth_mbps":0},"traffic":{"rx_bytes":0,"tx_bytes":0,"limit_bytes":null,"period_start":"2026-01-02T03:04:05Z","period_end":"2026-01-02T03:04:05Z"},"capabilities":{"power":false,"reinstall":false,"reset_password":false,"console":false,"rescue":false,"snapshots":false,"metrics":false,"upgrade":false}}`},
		{Console{"https://cloud.example/console/#ticket", at}, `{"url":"https://cloud.example/console/#ticket","expires_at":"2026-01-02T03:04:05Z"}`},
		{Metrics{300, []MetricPoint{{At: at}}}, `{"step_seconds":300,"points":[{"at":"2026-01-02T03:04:05Z","cpu_percent":null,"net_in_bps":null,"net_out_bps":null,"disk_read_bps":null,"disk_write_bps":null}]}`},
		{Snapshots{[]Snapshot{{testID, "before-update", "ready", nil, at}}, 3, true}, `{"items":[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"before-update","status":"ready","size_bytes":null,"created_at":"2026-01-02T03:04:05Z"}],"limit":3,"requires_stopped":true}`},
	}
	for _, tc := range cases {
		t.Run(reflect.TypeOf(tc.value).String(), func(t *testing.T) {
			raw, e := json.Marshal(tc.value)
			if e != nil || string(raw) != tc.want {
				t.Fatalf("wire changed: %s", raw)
			}
			out := reflect.New(reflect.TypeOf(tc.value))
			if Decode(raw, out.Interface()) != nil || !reflect.DeepEqual(out.Elem().Interface(), tc.value) || !required(raw, out.Elem().Type()) {
				t.Fatal("roundtrip failed")
			}
		})
	}
}
func TestDecodeRejectsAmbiguousMessages(t *testing.T) {
	for _, raw := range []string{`{"operation_id":"a","operation_id":"b"}`, `{"unknown":1}`, `{} {}`, "{\"code\":\"\xff\"}", `{"code":` + strings.Repeat("[", 20) + `0` + strings.Repeat("]", 20) + `}`} {
		var v ActionResult
		if Decode([]byte(raw), &v) == nil {
			t.Fatal("accepted malformed message")
		}
	}
	if required([]byte(`{"operation_id":"a","status":"accepted"}`), reflect.TypeOf(ActionResult{})) {
		t.Fatal("missing required field")
	}
	if required([]byte(`null`), reflect.TypeOf([]Image{})) {
		t.Fatal("null collection")
	}
}
