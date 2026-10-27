package pluginserver

import (
	"aster/billing/controllerapi"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/resourcev1"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"time"
)

// ResourceServer is a separate host-only resource boundary. The production
// launcher pins client UID 0 and the controller ID; the web API has no access.
type ResourceServer struct {
	Manifest     pluginapi.Manifest
	ClientUID    uint32
	ControllerID string
	Processor    resourcev1.Processor
	operations   chan struct{}
}

func (s ResourceServer) Serve(ctx context.Context, listener *net.UnixListener) error {
	if ctx == nil || listener == nil || s.Processor == nil || !pluginapi.ValidControllerID(s.ControllerID) || s.Manifest.Protocol != pluginapi.ResourceProtocol || s.Manifest.Validate(s.Manifest.CoreMin) != nil {
		return pluginapi.ErrInvalid
	}
	s.operations = make(chan struct{}, 8)
	server := &http.Server{Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 4096, ErrorLog: log.New(io.Discard, "", 0), ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, sessionKey{}, &session{})
	}}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	err := server.Serve(&boundedListener{Listener: listener, clientUID: s.ClientUID, slots: make(chan struct{}, 8)})
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (s ResourceServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	state, ok := r.Context().Value(sessionKey{}).(*session)
	if !ok {
		http.Error(w, "unavailable", 503)
		return
	}
	if r.URL.Path == "/v1/handshake" {
		var h pluginapi.Hello
		if !readJSON(w, r, &h) {
			return
		}
		if state.nonce != "" || h.Protocol != pluginapi.ResourceProtocol || !pluginapi.ValidDigest(h.Nonce) || s.Manifest.Validate(h.CoreVersion) != nil {
			http.Error(w, "incompatible", 409)
			return
		}
		state.nonce = h.Nonce
		reply(w, pluginapi.Identity{Protocol: s.Manifest.Protocol, ID: s.Manifest.ID, Version: s.Manifest.Version, SHA256: s.Manifest.SHA256, Capabilities: s.Manifest.Capabilities, Nonce: h.Nonce})
		return
	}
	if r.URL.Path != "/v1/resource/send" && r.URL.Path != "/v1/resource/lookup" {
		http.Error(w, "not found", 404)
		return
	}
	if state.nonce == "" || state.used {
		http.Error(w, "handshake required", 409)
		return
	}
	state.used = true // An invalid operation consumes the connection too.
	var envelope pluginapi.ResourceRequest
	if !readJSON(w, r, &envelope) {
		return
	}
	if envelope.Protocol != pluginapi.ResourceProtocol || envelope.Version != s.Manifest.Version || envelope.RequestID != state.nonce || envelope.ControllerID != s.ControllerID || envelope.TimeoutMS < 1 || envelope.TimeoutMS > 12000 {
		http.Error(w, "incompatible", 409)
		return
	}
	in, err := resourcev1.DecodeRequest(envelope.Payload)
	if err != nil || in.Product.ControllerID != s.ControllerID {
		http.Error(w, "incompatible", 409)
		return
	}
	select {
	case s.operations <- struct{}{}:
		defer func() { <-s.operations }()
	default:
		http.Error(w, "unavailable", 503)
		return
	}
	out := pluginapi.ResourceResponse{Protocol: pluginapi.ResourceProtocol, RequestID: envelope.RequestID, Status: "uncertain", Code: "unavailable", Payload: []byte{}}
	defer func() {
		if recover() != nil {
			reply(w, out)
		}
	}()
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(envelope.TimeoutMS)*time.Millisecond)
	defer cancel()
	var result controllerapi.Result
	// Lookup must not be used as a pre-send probe: exactly one selected handler.
	if r.URL.Path == "/v1/resource/send" {
		result, err = s.Processor.Send(ctx, in)
	} else {
		result, err = s.Processor.Lookup(ctx, in)
	}
	if err == nil && ctx.Err() == nil && result.Matches(in) {
		payload, e := json.Marshal(result)
		if e == nil {
			out.Status = "confirmed"
			out.Code = ""
			out.Payload = payload
		}
	}
	reply(w, out)
}
