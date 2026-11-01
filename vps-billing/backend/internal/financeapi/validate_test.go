package financeapi

import "testing"

func TestRequestContractRequiredNullableAndHostname(t *testing.T) {
	for _, tc := range []struct {
		name, operation, body string
		valid                 bool
	}{
		{"required currency", "createQuote", `{"items":[]}`, false},
		{"hostname lookahead", "createQuote", `{"items":[{"product_id":"starter","cycle":"monthly","quantity":1,"options":{},"hostname":"node.example"}],"currency":"USD"}`, true},
		{"invalid hostname", "createQuote", `{"items":[{"product_id":"starter","cycle":"monthly","quantity":1,"options":{},"hostname":"-node"}],"currency":"USD"}`, false},
		{"negative quantity", "createQuote", `{"items":[{"product_id":"starter","cycle":"monthly","quantity":-1,"options":{}}],"currency":"JPY"}`, false},
		{"nullable refund", "adminRefund", `{"payment_id":"p1","amount_minor":null,"destination":"credit","reason":"test"}`, false},
		{"three digit precision", "adminUpdateOsImage", `{"enabled":false,"name":"Ubuntu"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateInput(tc.operation, []byte(tc.body))
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}
