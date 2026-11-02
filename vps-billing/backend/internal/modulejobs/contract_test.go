package modulejobs

import "testing"

func TestModuleConfigurationArraySnapshot(t *testing.T) {
	source := ProductSpec{Mode: "module", ModuleID: "server.example", ConnectionID: "one", Config: map[string]any{"features": []any{"backup", "ipv6"}, "ports": []any{float64(22), float64(443)}, "empty": []any{}}}
	if !source.Valid() {
		t.Fatal("flat enum arrays rejected")
	}
	snapshot := source.Clone()
	before, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	source.Config["features"].([]any)[0] = "changed"
	after, err := snapshot.Digest()
	if err != nil || before != after {
		t.Fatal("snapshot aliases mutable array")
	}
	for _, invalid := range []any{[]any{"ipv6", "ipv6"}, []any{"ipv6", true}, []any{map[string]any{"nested": true}}} {
		source.Config["features"] = invalid
		if source.Valid() {
			t.Fatal("invalid flat enum array accepted")
		}
	}
}
