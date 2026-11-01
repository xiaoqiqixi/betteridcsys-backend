package runtimeplugins

import (
	"aster/billing/pkg/pluginapi"
	"context"
	"errors"
	"sync"
	"time"
)

type Gateway struct {
	Manifest      pluginapi.Manifest
	Selection     pluginapi.Selection
	Status        string
	LastCheckedAt *time.Time
}

func (m *Manager) paymentSelections() ([]pluginapi.Selection, error) {
	if m == nil || m.root == "" && m.policy == "" {
		return nil, nil
	}
	raw, err := readProtected(m.policy, pluginapi.MaxPolicyBytes, m.rules)
	if err != nil {
		return nil, err
	}
	var p pluginapi.Policy
	if pluginapi.DecodeStrict(raw, pluginapi.MaxPolicyBytes, &p) != nil {
		return nil, ErrUnavailable
	}
	err = validatePolicyKind(p, "payment", m.rules)
	if err != nil && !errors.Is(err, ErrDisabled) {
		return nil, err
	}
	if p.Payment != nil {
		return []pluginapi.Selection{*p.Payment}, nil
	}
	return p.Payments, nil
}

// Gateway health is a bounded handshake, never a provider charge or query.
// At most eight slots are checked concurrently and the caller can cancel all.
func (m *Manager) PaymentGateways(ctx context.Context) ([]Gateway, error) {
	selections, err := m.paymentSelections()
	if err != nil {
		return nil, err
	}
	out := make([]Gateway, len(selections))
	var wg sync.WaitGroup
	capacity := make(chan struct{}, 8)
	for index, selection := range selections {
		wg.Add(1)
		go func(i int, s pluginapi.Selection) {
			defer wg.Done()
			g := Gateway{Selection: s, Status: "misconfigured"}
			defer func() { out[i] = g }()
			select {
			case capacity <- struct{}{}:
				defer func() { <-capacity }()
			case <-ctx.Done():
				return
			}
			kind := "payment"
			if s.Slot != "" {
				kind += ":" + s.Slot
			}
			a, e := loadApprovalKind(m.root, m.policy, kind, m.rules, true)
			if a != nil {
				defer a.Close()
				g.Manifest = a.Manifest
				g.Selection = a.Selection
			}
			if errors.Is(e, ErrIncompatible) {
				g.Status = "incompatible"
				return
			}
			if e != nil {
				return
			}
			if !a.Selection.Enabled {
				g.Status = "stopped"
				return
			}
			check, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			c, _, e := handshake(check, a, m.rules)
			now := time.Now().UTC()
			g.LastCheckedAt = &now
			if e != nil {
				g.Status = "stopped"
				return
			}
			c.close()
			g.Status = "ready"
		}(index, selection)
	}
	wg.Wait()
	return out, nil
}
