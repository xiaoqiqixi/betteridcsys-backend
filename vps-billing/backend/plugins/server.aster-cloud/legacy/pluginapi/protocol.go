// Package pluginapi preserves the pre-module resource wire solely inside the
// Aster Cloud plugin. Finance Core does not load or select this protocol.
package pluginapi

import (
	core "aster/billing/pkg/pluginapi"
	"regexp"
	"strings"
)

const (
	CoreVersion            = core.CoreVersion
	ResourceProtocol       = "aster.resource/1"
	ResourceMinCoreVersion = "0.5.10"
	SMTPProtocol           = core.SMTPProtocol
	SMTPCapability         = core.SMTPCapability
	PaymentProtocol        = core.PaymentProtocol
	ServerProtocol         = core.ServerProtocol
	MaxRequestBytes        = core.MaxRequestBytes
	MaxResponseBytes       = core.MaxResponseBytes
	MaxManifestBytes       = core.MaxManifestBytes
	MaxPolicyBytes         = core.MaxPolicyBytes
)

var ErrInvalid = core.ErrInvalid
var ValidVersion = core.ValidVersion
var ValidDigest = core.ValidDigest
var ValidGatewayID = core.ValidGatewayID
var ValidConnectionID = core.ValidConnectionID
var CompareVersion = core.CompareVersion
var EqualCapabilities = core.EqualCapabilities
var PaymentCapabilities = core.PaymentCapabilities
var resourceID = regexp.MustCompile(`^resource\.[a-z][a-z0-9-]{0,47}$`)

func ValidID(id string) bool { return core.ValidID(id) || resourceID.MatchString(id) }

type Target = core.Target
type Hello = core.Hello
type Identity = core.Identity
type Manifest core.Manifest

func (m Manifest) Validate(version string) error {
	if m.Protocol != ResourceProtocol {
		return core.Manifest(m).Validate(version)
	}
	if m.Schema != 1 || !resourceID.MatchString(m.ID) || !ValidVersion(m.Version) || m.Executable != "plugin" || !ValidDigest(m.SHA256) || m.Payment != nil || !EqualCapabilities(m.Capabilities, ResourceCapabilities()) {
		return ErrInvalid
	}
	actual, a := CompareVersion(version, ResourceMinCoreVersion)
	declared, d := CompareVersion(m.CoreMin, ResourceMinCoreVersion)
	minimum, b := CompareVersion(version, m.CoreMin)
	maximum, c := CompareVersion(version, m.CoreMaxExclusive)
	if a != nil || b != nil || c != nil || d != nil || actual < 0 || declared < 0 || minimum < 0 || maximum >= 0 {
		return ErrInvalid
	}
	return nil
}

type Policy struct {
	Schema   int         `json:"schema"`
	Revision uint64      `json:"revision"`
	SMTP     *Selection  `json:"smtp,omitempty"`
	Payment  *Selection  `json:"payment,omitempty"`
	Payments []Selection `json:"payments,omitempty"`
	Servers  []Selection `json:"servers,omitempty"`
	Resource *Selection  `json:"resource,omitempty"`
}

type Selection struct {
	Slot           string   `json:"slot,omitempty"`
	Enabled        bool     `json:"enabled"`
	ID             string   `json:"id"`
	Version        string   `json:"version"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	WorkerUID      uint32   `json:"worker_uid"`
	ClientUID      uint32   `json:"client_uid"`
	Socket         string   `json:"socket"`
	AllowedTargets []Target `json:"allowed_targets"`
	MerchantID     string   `json:"merchant_id,omitempty"`
	ControllerID   string   `json:"controller_id,omitempty"`
}

func DecodeStrict(raw []byte, limit int, destination any) error {
	if out, ok := destination.(*ResourceResponse); ok {
		var envelope core.ServerResponse
		if err := core.DecodeStrict(raw, limit, &envelope); err != nil {
			return err
		}
		*out = ResourceResponse{Protocol: envelope.Protocol, RequestID: envelope.RequestID, Status: envelope.Status, Code: envelope.Code, Payload: envelope.Payload}
		return nil
	}
	return core.DecodeStrict(raw, limit, destination)
}

// Keep the compatibility identifier grammar separate from the core namespace.
func IsResource(id string) bool { return strings.HasPrefix(id, "resource.") && ValidID(id) }
