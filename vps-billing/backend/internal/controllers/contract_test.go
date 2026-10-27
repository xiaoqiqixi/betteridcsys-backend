package controllers

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixtureRequest() Request {
	p := ProductSpec{Mode: "custom", ControllerID: "primary", Resources: &Resources{DatacenterID: "dc-1", ImageID: "debian-12", CPUCores: 2, MemoryMiB: 2048, SystemDiskGiB: 30, NetworkType: "normal", TrafficDirection: "all", IPv4Count: 1}}
	h, _ := p.Digest()
	return Request{Protocol: Protocol, OperationID: strings.Repeat("a", 32), OrderID: strings.Repeat("b", 32), OwnerRef: strings.Repeat("c", 32), Hostname: "web-1", ConfigurationRevision: 2, ConfigurationSHA256: h, Product: p}
}

func fixtureResult(r Request) Result {
	_, h, _ := r.BytesAndDigest()
	return Result{Protocol: Protocol, OperationID: r.OperationID, OrderID: r.OrderID, RequestSHA256: h, Status: "succeeded", ResourceID: "vm-1"}
}

func TestProductContract(t *testing.T) {
	r := fixtureRequest()
	if !r.Valid() || !DisabledSpec().Valid() || !(ProductSpec{Mode: "preset", ControllerID: "primary", RemoteProductID: "sku-1"}).Valid() {
		t.Fatal("valid contracts rejected")
	}
	mutations := map[string]func(*ProductSpec){
		"URL as controller":          func(s *ProductSpec) { s.ControllerID = "https://example.invalid" },
		"unknown mode":               func(s *ProductSpec) { s.Mode = "automatic" },
		"disabled retains resources": func(s *ProductSpec) { s.Mode = "disabled" },
		"preset retains resources":   func(s *ProductSpec) { s.Mode = "preset"; s.RemoteProductID = "sku" },
		"both preset and custom":     func(s *ProductSpec) { s.RemoteProductID = "sku" },
		"missing resource placement": func(s *ProductSpec) { s.Resources.DatacenterID = "" },
		"resource injection":         func(s *ProductSpec) { s.Resources.ImageID = "../../secret" },
		"zero CPU":                   func(s *ProductSpec) { s.Resources.CPUCores = 0 },
		"memory granularity":         func(s *ProductSpec) { s.Resources.MemoryMiB = 129 },
		"negative disk":              func(s *ProductSpec) { s.Resources.DataDiskGiB = -1 },
		"orphan storage":             func(s *ProductSpec) { s.Resources.DataStorageID = "store" },
		"network":                    func(s *ProductSpec) { s.Resources.NetworkType = "shell" },
		"traffic":                    func(s *ProductSpec) { s.Resources.TrafficDirection = "both" },
		"no IP":                      func(s *ProductSpec) { s.Resources.IPv4Count = 0 },
		"too many IPs":               func(s *ProductSpec) { s.Resources.IPv6Count = 257 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := r.Product.Clone()
			mutate(&s)
			if s.Valid() {
				t.Fatal("invalid spec accepted")
			}
			if _, err := s.Digest(); err != ErrConfiguration {
				t.Fatal(err)
			}
		})
	}
	copy := r.Product.Clone()
	copy.Resources.MemoryMiB = 4096
	if r.Product.Resources.MemoryMiB != 2048 {
		t.Fatal("clone aliases mutable resources")
	}
	b, h, err := r.BytesAndDigest()
	if err != nil || len(h) != 64 || !json.Valid(b) {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"price", "currency", "email", "password", "token", "invoice"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatal("private/financial transport field", forbidden)
		}
	}
	r.Product.Resources.CPUCores++
	if r.Valid() {
		t.Fatal("changed snapshot accepted with old digest")
	}
}

func TestResultBindsExactRequest(t *testing.T) {
	r := fixtureRequest()
	good := fixtureResult(r)
	if !good.Matches(r) {
		t.Fatal("valid result rejected")
	}
	for _, status := range []string{"accepted", "rejected"} {
		v := good
		v.Status = status
		v.ResourceID = ""
		if !v.Matches(r) {
			t.Fatal(status)
		}
	}
	for name, mutate := range map[string]func(*Result){
		"protocol":               func(v *Result) { v.Protocol = "other/1" },
		"operation":              func(v *Result) { v.OperationID = strings.Repeat("d", 32) },
		"order":                  func(v *Result) { v.OrderID = strings.Repeat("d", 32) },
		"hash":                   func(v *Result) { v.RequestSHA256 = strings.Repeat("0", 64) },
		"status":                 func(v *Result) { v.Status = "paid" },
		"missing resource":       func(v *Result) { v.ResourceID = "" },
		"secret URL":             func(v *Result) { v.ResourceID = "https://example.invalid/token" },
		"rejected with resource": func(v *Result) { v.Status = "rejected" },
	} {
		t.Run(name, func(t *testing.T) {
			v := good
			mutate(&v)
			if v.Matches(r) {
				t.Fatal("unbound result accepted")
			}
		})
	}
}
