package controllerapi

import (
	"aster/billing/internal/controllers"
	"context"
)

// These aliases expose the existing host-only transport, not a second client.
// Policy, credential and CA files retain the internal root-ownership, private
// mode, no-link and protected-ancestor checks. Request fields never pick a URL.
type Policy = controllers.Policy
type Profile = controllers.Profile
type Client = controllers.Client

var ErrConfiguration = controllers.ErrConfiguration
var ErrUnavailable = controllers.ErrUnavailable
var ErrUncertain = controllers.ErrUncertain

func LoadPolicy(path string) (Policy, error) { return controllers.LoadPolicy(path) }

// New selects one fixed, enabled destination from a root-protected policy.
// It retains CIDR-pinned DNS, verified HTTPS, no environment proxy/redirects,
// no physical-connection redial and the original exact six-field result guard.
func New(ctx context.Context, policyPath, controllerID string) (*Client, error) {
	policy, err := LoadPolicy(policyPath)
	if err != nil {
		return nil, err
	}
	return policy.Client(ctx, controllerID)
}

// NewCredentialTransport is only for a host-approved resource worker using
// fixed systemd credentials, never browser-selected endpoint/credentials.
// Profile file paths must be empty; the original HTTPS/CIDR/DNS/CA/no-redial
// validation and exact result guard are retained by the shared transport.
func NewCredentialTransport(ctx context.Context, profile Profile, token, ca []byte) (*Client, error) {
	return controllers.CredentialClient(ctx, profile, token, ca)
}
