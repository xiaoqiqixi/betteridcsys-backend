package migration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestControllerMigrationSelectsOneDirectConnectionWithoutSecrets(t *testing.T) {
	raw := []byte(`{"schema":1,"controllers":[{"id":"east","enabled":true,"endpoint":"https://cloud.example.test:8443/v1/provisioning","allowed_cidrs":["192.0.2.0/24"],"token_file":"/etc/aster/private/token","ca_file":"/etc/aster/private/ca.pem"},{"id":"west","enabled":false,"endpoint":"https://west.example.test/v1/provisioning","allowed_cidrs":["198.51.100.0/24"],"token_file":"/etc/aster/private/west-token","ca_file":""}]}`)
	plan, profile, err := PlanControllerMigration(raw, "east", "east-cloud")
	if err != nil || plan.ConnectionID != "east-cloud" || plan.Config.ControllerID != "east" || len(plan.Targets) != 1 || plan.Targets[0].Port != 8443 || profile.TokenFile == "" {
		t.Fatal(plan, err)
	}
	encoded, _ := json.Marshal(plan)
	if strings.Contains(string(encoded), "private/") || strings.Contains(string(encoded), "token") {
		t.Fatal("credential path escaped migration plan")
	}
	for _, id := range []string{"missing", ""} {
		if _, _, err := PlanControllerMigration(raw, id, "east-cloud"); err == nil {
			t.Fatal("ambiguous controller accepted")
		}
	}
	if _, _, err := PlanControllerMigration(raw, "east", "../other"); err == nil {
		t.Fatal("unsafe connection accepted")
	}
	if _, _, err := PlanControllerMigration([]byte(strings.Replace(string(raw), `"id":"west"`, `"id":"east"`, 1)), "east", "east-cloud"); err == nil {
		t.Fatal("duplicate controller accepted")
	}
}

func TestMigrationBindingUsesNormalizedModuleDigest(t *testing.T) {
	bound, err := migrationBinding("east", "small")
	if err != nil || bound.ModuleID != "server.aster-cloud" || bound.ConnectionID != "east" || len(bound.ConfigDigest) != 64 || bound.Config["preset_id"] != "small" {
		t.Fatal(bound, err)
	}
	if _, err := migrationBinding("east", ""); err == nil {
		t.Fatal("missing immutable preset accepted")
	}
}
