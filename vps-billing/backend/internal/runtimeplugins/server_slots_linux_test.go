package runtimeplugins

import (
	"aster/billing/pkg/pluginapi"
	"strings"
	"testing"
)

func TestServerConnectionsRequireIsolatedIdentitiesAndCanonicalSockets(t *testing.T) {
	selected := pluginapi.Selection{Slot: "east", ID: "server.fixture", Version: "1.0.0", Enabled: true, ManifestSHA256: strings.Repeat("a", 64), WorkerUID: 1002, ClientUID: 1001, Socket: "/run/aster-server/east/plugin.sock"}
	policy := pluginapi.Policy{Schema: 1, Revision: 1, Servers: []pluginapi.Selection{selected}}
	if e := validatePolicyKind(policy, "server:east", trust{}); e != nil {
		t.Fatal(e)
	}
	copy := selected
	copy.Slot = "west"
	copy.Socket = "/run/aster-server/west/plugin.sock"
	policy.Servers = append(policy.Servers, copy)
	if validatePolicyKind(policy, "server:east", trust{}) == nil {
		t.Fatal("shared UID accepted")
	}
	policy.Servers[1].WorkerUID = 1003
	if e := validatePolicyKind(policy, "server:west", trust{}); e != nil {
		t.Fatal(e)
	}
	policy.Servers[1].Socket = selected.Socket
	if validatePolicyKind(policy, "server:west", trust{}) == nil {
		t.Fatal("shared socket accepted")
	}
}
