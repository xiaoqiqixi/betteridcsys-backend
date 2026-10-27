package controllerapi

import "testing"

func TestPublishedContractIsTheBillingWireContract(t *testing.T) {

	spec := ProductSpec{Mode: "preset", ControllerID: "primary", RemoteProductID: "small"}
	digest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	r := Request{Protocol: Protocol, OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", OrderID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", OwnerRef: "cccccccccccccccccccccccccccccccc", Hostname: "test-vm", ConfigurationRevision: 1, ConfigurationSHA256: digest, Product: spec}
	_, requestDigest, err := r.BytesAndDigest()
	if err != nil {
		t.Fatal(err)
	}
	result := Result{Protocol: Protocol, OperationID: r.OperationID, OrderID: r.OrderID, RequestSHA256: requestDigest, Status: "accepted"}
	if !result.Matches(r) {
		t.Fatal("public contract no longer matches billing")
	}
}
