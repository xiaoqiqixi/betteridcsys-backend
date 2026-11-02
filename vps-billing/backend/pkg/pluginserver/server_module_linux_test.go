package pluginserver

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerRPCConsumesOneAuthenticatedRequestAndHidesErrors(t *testing.T) {
	calls := 0
	manifest := pluginapi.Manifest{Schema: 1, ID: "server.fixture", Version: "1.0.0", Protocol: pluginapi.ServerProtocol, CoreMin: "0.6.0", CoreMaxExclusive: "0.7.0", Capabilities: pluginapi.ServerCapabilities(), Executable: "plugin", SHA256: strings.Repeat("a", 64)}
	s := ServerServer{Manifest: manifest, ConnectionID: "east", operations: make(chan struct{}, 1), Processor: serverv1.ProcessorFunc(func(_ context.Context, in serverv1.Request) (serverv1.Response, error) {
		calls++
		return serverv1.Response{Protocol: serverv1.Protocol, State: "done"}, nil
	})}
	state := &session{nonce: strings.Repeat("b", 64)}
	payload, _ := json.Marshal(serverv1.Request{Protocol: serverv1.Protocol, Method: "test_connection"})
	envelope := pluginapi.ServerRequest{Protocol: pluginapi.ServerProtocol, Version: "1.0.0", ConnectionID: "east", RequestID: state.nonce, TimeoutMS: 1000, Payload: payload}
	raw, _ := json.Marshal(envelope)
	run := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://plugin.local/v1/server/call", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, state))
		w := httptest.NewRecorder()
		s.serveHTTP(w, r)
		return w
	}
	if w := run(); w.Code != 200 || calls != 1 {
		t.Fatal(w.Code, calls)
	}
	if w := run(); w.Code != 409 || calls != 1 {
		t.Fatal("connection replay", w.Code, calls)
	}
	state = &session{nonce: envelope.RequestID}
	envelope.ConnectionID = "west"
	raw, _ = json.Marshal(envelope)
	if w := run(); w.Code != 409 || calls != 1 {
		t.Fatal("cross-connection dispatch", w.Code, calls)
	}
}
