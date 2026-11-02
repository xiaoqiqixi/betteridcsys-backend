// Package serverv1 is the platform-neutral provisioning module protocol.
// It has no dependency on finance storage or a provider SDK.
package serverv1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

const Protocol = "aster.server/1"
const MaximumPayload = 512 << 10

var ErrConfiguration = errors.New("invalid server module request")
var ErrUnavailable = errors.New("server module unavailable")
var ErrUncertain = errors.New("server module outcome uncertain")
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var moduleIdentifier = regexp.MustCompile(`^server\.[a-z][a-z0-9-]{0,47}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidID(id string) bool    { return identifier.MatchString(id) }
func ValidDigest(d string) bool { return digestPattern.MatchString(d) }

type Text map[string]string
type Fields map[string]any
type CustomAction struct {
	ID          string `json:"id"`
	Name        Text   `json:"name"`
	Danger      bool   `json:"danger"`
	Confirm     Text   `json:"confirm"`
	InputSchema Schema `json:"input_schema"`
}
type Description struct {
	ID            string         `json:"id"`
	Name          Text           `json:"name"`
	Version       string         `json:"version"`
	Capabilities  []string       `json:"capabilities"`
	ProductSchema Schema         `json:"product_schema"`
	OrderSchema   Schema         `json:"order_schema"`
	CustomActions []CustomAction `json:"custom_actions"`
}

func (d Description) Has(method string) bool {
	for _, c := range d.Capabilities {
		if c == method {
			return true
		}
	}
	return false
}
func (d Description) Validate() error {
	if !moduleIdentifier.MatchString(d.ID) || d.Version == "" || !validText(d.Name) || len(d.Capabilities) > 32 || len(d.CustomActions) > 32 || d.ProductSchema.Validate() != nil || d.OrderSchema.Validate() != nil {
		return ErrConfiguration
	}
	seen := map[string]bool{}
	for _, c := range d.Capabilities {
		if !IsMutation(c) && !IsRead(c) || seen[c] {
			return ErrConfiguration
		}
		seen[c] = true
	}
	for _, f := range d.ProductSchema.Properties {
		if f.WriteOnly {
			return ErrConfiguration
		}
	}
	seen = map[string]bool{}
	for _, a := range d.CustomActions {
		if !ValidID(a.ID) || seen[a.ID] || !validText(a.Name) || a.InputSchema.Validate() != nil {
			return ErrConfiguration
		}
		seen[a.ID] = true
	}
	return nil
}
func validText(t Text) bool {
	return (t["en"] != "" || t["zh-CN"] != "") && len(t["en"]) <= 4000 && len(t["zh-CN"]) <= 4000
}

// Secrets are deliberately separate from durable configuration. A caller may
// retain Redacted plus the digest, never the original request or a retry copy.
type Request struct {
	Protocol    string `json:"protocol"`
	Method      string `json:"method"`
	OperationID string `json:"operation_id,omitempty"`
	Digest      string `json:"digest,omitempty"`
	ServiceID   string `json:"service_id,omitempty"`
	ResourceID  string `json:"resource_id,omitempty"`
	OwnerRef    string `json:"owner_ref,omitempty"`
	Config      Fields `json:"config,omitempty"`
	Input       Fields `json:"input,omitempty"`
	Secrets     Fields `json:"secrets,omitempty"`
	ActionID    string `json:"action_id,omitempty"`
	Range       string `json:"range,omitempty"`
}

func IsMutation(m string) bool {
	switch m {
	case "create", "suspend", "unsuspend", "terminate", "change_package", "renew", "power", "reinstall", "reset_password", "rescue_enter", "rescue_exit", "snapshot_create", "snapshot_restore", "snapshot_delete", "action":
		return true
	}
	return false
}
func IsRead(m string) bool {
	switch m {
	case "describe", "test_connection", "validate_product", "status", "operation_status", "console", "metrics", "snapshots", "images":
		return true
	}
	return false
}
func (r Request) Redacted() Request { r.Secrets = nil; return r }
func (r Request) Validate() error {
	if r.Protocol != Protocol || !IsMutation(r.Method) && !IsRead(r.Method) {
		return ErrConfiguration
	}
	if IsMutation(r.Method) && (!ValidID(r.OperationID) || !ValidID(r.ServiceID) || !ValidID(r.OwnerRef)) {
		return ErrConfiguration
	}
	if IsMutation(r.Method) && r.Method != "create" && !ValidID(r.ResourceID) {
		return ErrConfiguration
	}
	if r.Method == "operation_status" && (!ValidID(r.OperationID) || !ValidDigest(r.Digest)) {
		return ErrConfiguration
	}
	if r.Method == "action" && !ValidID(r.ActionID) {
		return ErrConfiguration
	}
	if r.Method == "metrics" && !ValidRange(r.Range) {
		return ErrConfiguration
	}
	if !IsMutation(r.Method) && len(r.Secrets) > 0 {
		return ErrConfiguration
	}
	return nil
}
func (r Request) BytesAndDigest() ([]byte, string, error) {
	if r.Validate() != nil {
		return nil, "", ErrConfiguration
	}
	r.Digest = ""
	b, err := json.Marshal(r)
	if err != nil || len(b) > MaximumPayload {
		return nil, "", ErrConfiguration
	}
	h := sha256.Sum256(b)
	return b, hex.EncodeToString(h[:]), nil
}
func (r *Request) Seal() error {
	_, d, e := r.BytesAndDigest()
	if e == nil {
		r.Digest = d
	}
	return e
}
func (r Request) Verify() error {
	if r.Validate() != nil {
		return ErrConfiguration
	}
	if IsMutation(r.Method) {
		_, d, e := r.BytesAndDigest()
		if e != nil || d != r.Digest {
			return ErrConfiguration
		}
	}
	return nil
}

type Response struct {
	Protocol     string       `json:"protocol"`
	OperationID  string       `json:"operation_id,omitempty"`
	Digest       string       `json:"digest,omitempty"`
	State        string       `json:"state"`          // done, accepted, failed or uncertain
	Code         string       `json:"code,omitempty"` // bounded machine code, never provider messages
	ResourceID   string       `json:"resource_id,omitempty"`
	Description  *Description `json:"description,omitempty"`
	Config       Fields       `json:"config,omitempty"`
	ConfigDigest string       `json:"config_digest,omitempty"`
	Service      *Service     `json:"service,omitempty"`
	Console      *Console     `json:"console,omitempty"`
	Metrics      *Metrics     `json:"metrics,omitempty"`
	Snapshots    *Snapshots   `json:"snapshots,omitempty"`
	Images       []Image      `json:"images,omitempty"`
}

func (r Response) Matches(in Request) bool {
	if r.Protocol != Protocol || (r.State != "done" && r.State != "accepted" && r.State != "failed" && r.State != "uncertain") || len(r.Code) > 64 {
		return false
	}
	if IsMutation(in.Method) || in.Method == "operation_status" {
		return r.OperationID == in.OperationID && r.Digest == in.Digest && ValidDigest(r.Digest)
	}
	return true
}

type Processor interface {
	Call(context.Context, Request) (Response, error)
}
type ProcessorFunc func(context.Context, Request) (Response, error)

func (f ProcessorFunc) Call(ctx context.Context, r Request) (Response, error) { return f(ctx, r) }

// The payload is canonical JSON inside the bounded runtime envelope. Rejecting
// duplicate keys and trailing data prevents ambiguity in idempotency digests.
func Decode(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > MaximumPayload {
		return ErrConfiguration
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if d.Decode(out) != nil {
		return ErrConfiguration
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrConfiguration
	}
	canonical, e := json.Marshal(out)
	if e != nil || !bytes.Equal(raw, canonical) {
		return ErrConfiguration
	}
	return nil
}
