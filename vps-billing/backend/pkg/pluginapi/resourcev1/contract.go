// Package resourcev1 exposes only the existing immutable resource contract.
// It grants no payment, database, filesystem or arbitrary command capability.
package resourcev1

import (
	"aster/billing/controllerapi"
	"bytes"
	"context"
	"encoding/json"
)

const MaximumPayload = 64 << 10

type Processor interface {
	Send(context.Context, controllerapi.Request) (controllerapi.Result, error)
	Lookup(context.Context, controllerapi.Request) (controllerapi.Result, error)
}

// Canonical payloads deliberately preserve the public request's nullable
// product.resources without weakening the outer runtime JSON parser. Exact
// re-encoding rejects omitted/unknown/duplicate/aliased fields and extra bytes.
func DecodeRequest(raw []byte) (controllerapi.Request, error) {
	var in controllerapi.Request
	if len(raw) == 0 || len(raw) > MaximumPayload || json.Unmarshal(raw, &in) != nil {
		return in, controllerapi.ErrConfiguration
	}
	canonical, _, err := in.BytesAndDigest()
	if err != nil || !bytes.Equal(raw, canonical) {
		return controllerapi.Request{}, controllerapi.ErrConfiguration
	}
	return in, nil
}
func DecodeResult(raw []byte, in controllerapi.Request) (controllerapi.Result, error) {
	var out controllerapi.Result
	if len(raw) == 0 || len(raw) > MaximumPayload || json.Unmarshal(raw, &out) != nil || !out.Matches(in) {
		return controllerapi.Result{}, controllerapi.ErrUncertain
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(raw, canonical) {
		return controllerapi.Result{}, controllerapi.ErrUncertain
	}
	return out, nil
}
