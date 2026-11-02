package financeapi

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/dlclark/regexp2/v2"
)

func TestEveryPinnedRequestSchemaCompiles(t *testing.T) {
	_ = ValidateInput("login", []byte(`{}`))
	if schemaError != nil {
		t.Fatal(schemaError)
	}
	var document struct {
		Requests map[string]json.RawMessage `json:"requests"`
	}
	if err := json.Unmarshal([]byte(validationDocument), &document); err != nil {
		t.Fatal(err)
	}
	for operation := range document.Requests {
		t.Run(operation, func(t *testing.T) {
			schemaMutex.Lock()
			_, err := schemaCompiler.Compile("https://contract.aster.invalid/requests#/requests/" + operation)
			schemaMutex.Unlock()
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

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

func TestRequestValidationAfterIdleClockRestart(t *testing.T) {
	const body = `{"items":[{"product_id":"starter","cycle":"monthly","quantity":1,"options":{},"hostname":"node.example"}],"currency":"USD"}`
	if err := ValidateInput("createQuote", []byte(body)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(regexp2.StopTimeoutClock)
	for round := range 2 {
		// Reproduce the stopped timeout clock between otherwise valid requests.
		regexp2.StopTimeoutClock()
		time.Sleep(400 * time.Millisecond)
		start := make(chan struct{})
		errs := make(chan error, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				until := time.Now().Add(300 * time.Millisecond)
				for time.Now().Before(until) {
					if err := ValidateInput("createQuote", []byte(body)); err != nil {
						errs <- err
						return
					}
					time.Sleep(time.Millisecond)
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("valid request rejected after idle round %d: %v", round, err)
		}
	}
}
