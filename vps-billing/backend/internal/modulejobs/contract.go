// Package modulejobs stores platform-neutral snapshots for the legacy job API.
// It is storage metadata; the runtime converts it to aster.server/1 requests.
package modulejobs

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
)

const Protocol = "aster.module-job/1"

var ErrConfiguration = errors.New("invalid module job configuration")
var ErrUnavailable = serverv1.ErrUnavailable
var ErrUncertain = serverv1.ErrUncertain
var operationID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var hostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
var moduleID = regexp.MustCompile(`^server\.[a-z][a-z0-9-]{0,47}$`)

type ProductSpec struct {
	Mode         string          `json:"mode"`
	ModuleID     string          `json:"module_id"`
	ConnectionID string          `json:"connection_id"`
	Config       serverv1.Fields `json:"config"`
}

func DisabledSpec() ProductSpec            { return ProductSpec{Mode: "disabled"} }
func validID(v string, required bool) bool { return v == "" && !required || serverv1.ValidID(v) }
func (s ProductSpec) Valid() bool {
	if s.Mode == "disabled" {
		return s.ModuleID == "" && s.ConnectionID == "" && s.Config == nil
	}
	if s.Mode != "module" || !moduleID.MatchString(s.ModuleID) || !pluginapi.ValidConnectionID(s.ConnectionID) || s.Config == nil || len(s.Config) > 64 {
		return false
	}
	schema := serverv1.EmptySchema()
	for k, v := range s.Config {
		f, ok := configField(v)
		if !ok {
			return false
		}
		schema.Properties[k] = f
	}
	_, _, err := schema.Normalize(s.Config)
	return err == nil
}
func configField(v any) (serverv1.Field, bool) {
	f := serverv1.Field{}
	switch value := v.(type) {
	case string:
		f.Type = "string"
	case bool:
		f.Type = "boolean"
	case float64, int, int64, json.Number:
		f.Type = "number"
	case []any:
		if len(value) > 100 {
			return f, false
		}
		item := serverv1.Field{Type: "string", Enum: []any{""}}
		if len(value) > 0 {
			var ok bool
			item, ok = configField(value[0])
			if !ok || item.Type == "array" {
				return f, false
			}
			item.Enum = append([]any{}, value...)
		}
		f.Type, f.Items = "array", &item
	default:
		return f, false
	}
	return f, true
}
func (s ProductSpec) Clone() ProductSpec { s.Config = cloneFields(s.Config); return s }
func cloneFields(in serverv1.Fields) serverv1.Fields {
	if in == nil {
		return nil
	}
	out := serverv1.Fields{}
	for k, v := range in {
		if values, ok := v.([]any); ok {
			out[k] = append([]any{}, values...)
		} else {
			out[k] = v
		}
	}
	return out
}
func (s ProductSpec) Digest() (string, error) {
	if !s.Valid() {
		return "", ErrConfiguration
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", ErrConfiguration
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}

// Request intentionally excludes money, invoice data, personal details,
// account credentials and current catalog labels. It is built from an
// immutable order snapshot, never from today's product configuration.
type Request struct {
	Protocol              string      `json:"protocol"`
	OperationID           string      `json:"operation_id"`
	OrderID               string      `json:"order_id"`
	OwnerRef              string      `json:"owner_ref"`
	Hostname              string      `json:"hostname"`
	ConfigurationRevision int64       `json:"configuration_revision"`
	ConfigurationSHA256   string      `json:"configuration_sha256"`
	Product               ProductSpec `json:"product"`
}

func (r Request) Valid() bool {
	if r.Protocol != Protocol || !operationID.MatchString(r.OperationID) || !operationID.MatchString(r.OrderID) || !operationID.MatchString(r.OwnerRef) || r.ConfigurationRevision < 1 || r.ConfigurationRevision > 9007199254740991 || !hashPattern.MatchString(r.ConfigurationSHA256) || !r.Product.Valid() || r.Product.Mode == "disabled" {
		return false
	}
	if !hostnamePattern.MatchString(r.Hostname) {
		return false
	}
	h, err := r.Product.Digest()
	return err == nil && h == r.ConfigurationSHA256
}

func (r Request) BytesAndDigest() ([]byte, string, error) {
	if !r.Valid() {
		return nil, "", ErrConfiguration
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, "", ErrConfiguration
	}
	h := sha256.Sum256(b)
	return b, hex.EncodeToString(h[:]), nil
}

// Result must bind the exact operation, order and request bytes. Accepted
// means the module accepted a task, not that a service is active. Even a
// succeeded result does not manufacture a local Service or paid record.
type Result struct {
	Protocol      string `json:"protocol"`
	OperationID   string `json:"operation_id"`
	OrderID       string `json:"order_id"`
	RequestSHA256 string `json:"request_sha256"`
	Status        string `json:"status"` // accepted, succeeded, rejected
	ResourceID    string `json:"resource_id"`
}

func (r Result) Matches(request Request) bool {
	_, h, err := request.BytesAndDigest()
	if err != nil || r.Protocol != Protocol || r.OperationID != request.OperationID || r.OrderID != request.OrderID || r.RequestSHA256 != h {
		return false
	}
	switch r.Status {
	case "accepted":
		return validID(r.ResourceID, false)
	case "succeeded":
		return validID(r.ResourceID, true)
	case "rejected":
		return r.ResourceID == ""
	}
	return false
}
