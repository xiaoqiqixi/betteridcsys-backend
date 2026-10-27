package pluginserver

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"time"
)

type PaymentServer struct {
	Manifest   pluginapi.Manifest
	ClientUID  uint32
	MerchantID string
	Processor  paymentv1.Processor
	operations chan struct{}
}

func (s PaymentServer) Serve(ctx context.Context, listener *net.UnixListener) error {
	if ctx == nil || listener == nil || s.Processor == nil || s.Manifest.Protocol != pluginapi.PaymentProtocol || s.Manifest.Validate(s.Manifest.CoreMin) != nil || !paymentv1.ValidID(s.MerchantID) {
		return pluginapi.ErrInvalid
	}
	s.operations = make(chan struct{}, 8)
	server := &http.Server{Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 4096, ErrorLog: log.New(io.Discard, "", 0), ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, sessionKey{}, &session{})
	}}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	e := server.Serve(&boundedListener{Listener: listener, clientUID: s.ClientUID, slots: make(chan struct{}, 8)})
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func (s PaymentServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
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
		if state.nonce != "" || h.Protocol != pluginapi.PaymentProtocol || !pluginapi.ValidDigest(h.Nonce) || s.Manifest.Validate(h.CoreVersion) != nil {
			http.Error(w, "incompatible", 409)
			return
		}
		state.nonce = h.Nonce
		reply(w, pluginapi.Identity{Protocol: s.Manifest.Protocol, ID: s.Manifest.ID, Version: s.Manifest.Version, SHA256: s.Manifest.SHA256, Capabilities: s.Manifest.Capabilities, Nonce: h.Nonce})
		return
	}
	if r.URL.Path != "/v1/payment/checkout" && r.URL.Path != "/v1/payment/verify" {
		http.Error(w, "not found", 404)
		return
	}
	if state.nonce == "" || state.used {
		http.Error(w, "handshake required", 409)
		return
	}
	state.used = true
	select {
	case s.operations <- struct{}{}:
		defer func() { <-s.operations }()
	default:
		http.Error(w, "unavailable", 503)
		return
	}
	if r.URL.Path == "/v1/payment/checkout" {
		s.create(w, r, state)
		return
	}
	s.verify(w, r, state)
}
func (s PaymentServer) allowed(protocol, version, nonce string, timeout int64, cfg paymentv1.Config, state *session) bool {
	return protocol == pluginapi.PaymentProtocol && version == s.Manifest.Version && nonce == state.nonce && timeout >= 1 && timeout <= 12000 && paymentv1.ValidateConfig(cfg) == nil && cfg.ProviderID == s.Manifest.ID && cfg.MerchantID == s.MerchantID
}
func (s PaymentServer) create(w http.ResponseWriter, r *http.Request, state *session) {
	var in pluginapi.PaymentCreateRequest
	if !readJSON(w, r, &in) {
		return
	}
	if !s.allowed(in.Protocol, in.Version, in.RequestID, in.TimeoutMS, in.Config, state) || paymentv1.ValidateIntent(in.Intent, *s.Manifest.Payment) != nil {
		http.Error(w, "incompatible", 409)
		return
	}
	out := pluginapi.PaymentCreateResponse{Protocol: pluginapi.PaymentProtocol, RequestID: in.RequestID, Status: "failed", Code: "unavailable"}
	defer func() {
		if recover() != nil {
			reply(w, out)
		}
	}()
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(in.TimeoutMS)*time.Millisecond)
	defer cancel()
	checkout, e := s.Processor.Create(ctx, in.Config, in.Intent)
	if e == nil && ctx.Err() == nil && paymentv1.ValidateCheckout(checkout, s.Manifest.ID, *s.Manifest.Payment, time.Now()) == nil {
		out.Status = "accepted"
		out.Code = ""
		out.Checkout = &checkout
	} else if errors.Is(e, paymentv1.ErrRejected) || errors.Is(e, paymentv1.ErrInvalid) {
		out.Code = "rejected"
	}
	reply(w, out)
}
func (s PaymentServer) verify(w http.ResponseWriter, r *http.Request, state *session) {
	var in pluginapi.PaymentVerifyRequest
	if !readJSON(w, r, &in) {
		return
	}
	if !s.allowed(in.Protocol, in.Version, in.RequestID, in.TimeoutMS, in.Config, state) || paymentv1.ValidateCallback(in.Callback) != nil {
		http.Error(w, "incompatible", 409)
		return
	}
	out := pluginapi.PaymentVerifyResponse{Protocol: pluginapi.PaymentProtocol, RequestID: in.RequestID, Status: "failed", Code: "unavailable"}
	defer func() {
		if recover() != nil {
			reply(w, out)
		}
	}()
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(in.TimeoutMS)*time.Millisecond)
	defer cancel()
	event, e := s.Processor.Verify(ctx, in.Config, in.Callback)
	if e == nil && ctx.Err() == nil && paymentv1.ValidateEvent(event, s.Manifest.ID, s.MerchantID, *s.Manifest.Payment, time.Now()) == nil {
		out.Status = "verified"
		out.Code = ""
		out.Event = &event
	} else if errors.Is(e, paymentv1.ErrRejected) || errors.Is(e, paymentv1.ErrInvalid) {
		out.Code = "rejected"
	}
	reply(w, out)
}
