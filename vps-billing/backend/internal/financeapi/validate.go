package financeapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var schemaOnce sync.Once
var schemaCompiler *jsonschema.Compiler
var schemaError error
var schemaMutex sync.Mutex

type ecmaRegexp struct{ expression *regexp2.Regexp }

func (r ecmaRegexp) String() string { return r.expression.String() }

func (r ecmaRegexp) MatchString(s string) bool {
	matched, err := r.expression.MatchString(s)
	return err == nil && matched
}

// ValidateInput applies the exact pinned request schema before typed decoding.
// Errors deliberately contain no request values: bodies may contain passwords.
func ValidateInput(operation string, raw []byte) error {
	schemaOnce.Do(func() {
		var doc any
		schemaError = json.Unmarshal([]byte(validationDocument), &doc)
		if schemaError != nil {
			return
		}
		schemaCompiler = jsonschema.NewCompiler()
		schemaCompiler.DefaultDraft(jsonschema.Draft2020)
		schemaCompiler.AssertFormat()
		schemaCompiler.UseRegexpEngine(func(pattern string) (jsonschema.Regexp, error) {
			r, err := regexp2.Compile(pattern, regexp2.ECMAScript)
			if err != nil {
				return nil, err
			}
			r.MatchTimeout = 100 * time.Millisecond
			return ecmaRegexp{r}, nil
		})
		schemaError = schemaCompiler.AddResource("https://contract.aster.invalid/requests", doc)
	})
	if schemaError != nil {
		return fmt.Errorf("request schema unavailable")
	}
	schemaMutex.Lock()
	schema, err := schemaCompiler.Compile("https://contract.aster.invalid/requests#/requests/" + operation)
	schemaMutex.Unlock()
	if err != nil {
		return fmt.Errorf("request schema unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || schema.Validate(value) != nil {
		return fmt.Errorf("request does not match contract")
	}
	return nil
}
