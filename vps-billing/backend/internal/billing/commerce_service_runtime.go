package billing

import (
	"aster/billing/controllerapi"
	"aster/billing/internal/controllers"
	"aster/billing/serviceapi"
	"context"
	"sort"
)

// LoadServiceControllers06 accepts a root-controlled policy, separate from
// product metadata and the legacy host CLI's private provisioning policy.
func LoadServiceControllers06(path string) (*ServiceControllers06, error) {
	if path == "" {
		return nil, nil
	}
	credentials, err := controllers.LoadRuntimeCredentials(path)
	if err != nil {
		return nil, err
	}
	entries := map[string]controllers.RuntimeCredential{}
	ids := []string{}
	for _, entry := range credentials {
		entries[entry.Profile.ID] = entry
		ids = append(ids, entry.Profile.ID)
	}
	sort.Strings(ids)
	return &ServiceControllers06{IDs: ids,
		Services: func(ctx context.Context, id string) (ServiceController06, error) {
			entry, ok := entries[id]
			if !ok {
				return nil, serviceapi.ErrUnavailable
			}
			return serviceapi.NewCredentialTransport(ctx, entry.Profile, entry.Token, entry.CA)
		},
		Provisioning: func(ctx context.Context, id string) (ProvisioningController06, error) {
			entry, ok := entries[id]
			if !ok {
				return nil, controllerapi.ErrUnavailable
			}
			return controllerapi.NewCredentialTransport(ctx, entry.Profile, entry.Token, entry.CA)
		},
	}, nil
}
