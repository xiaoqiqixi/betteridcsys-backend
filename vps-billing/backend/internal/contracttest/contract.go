// Package contracttest validates handler responses against the pinned frontend
// OpenAPI 3.1 contract. It is imported by tests only, never by the server.
package contracttest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const resource = "https://contract.aster.invalid/openapi.json"

var once sync.Once
var compiler *jsonschema.Compiler
var loadError error
var mu sync.Mutex

func load() {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "../../../contract")
	// Relocated integration binaries still validate the exact hash-pinned files.
	if relocated := os.Getenv("ASTER_TEST_CONTRACT_DIR"); relocated != "" {
		dir = relocated
	}
	raw, err := os.ReadFile(filepath.Join(dir, "openapi.json"))
	if err != nil {
		loadError = err
		return
	}
	metadata, err := os.ReadFile(filepath.Join(dir, "SOURCE.json"))
	if err != nil {
		loadError = err
		return
	}
	var source struct {
		SHA256 string `json:"sha256"`
	}
	if err = json.Unmarshal(metadata, &source); err != nil {
		loadError = err
		return
	}
	hash := sha256.Sum256(raw)
	if source.SHA256 != hex.EncodeToString(hash[:]) {
		loadError = fmt.Errorf("vendored contract digest mismatch")
		return
	}
	var document any
	if err = json.Unmarshal(raw, &document); err != nil {
		loadError = err
		return
	}
	compiler = jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	loadError = compiler.AddResource(resource, document)
}

func validate(pointer string, value any) error {
	once.Do(load)
	if loadError != nil {
		return loadError
	}
	mu.Lock()
	schema, err := compiler.Compile(resource + "#" + pointer)
	mu.Unlock()
	if err != nil {
		return err
	}
	// Round-trip through JSON: validation must see exactly what a client sees,
	// including omitted fields, null slices, integers and custom marshalers.
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var data any
	if err := d.Decode(&data); err != nil {
		return err
	}
	return schema.Validate(data)
}

func escape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

// Data validates an already-decoded success envelope against a named schema.
func Data(t testing.TB, name string, envelope map[string]any) {
	t.Helper()
	data, ok := envelope["data"]
	if !ok {
		t.Fatalf("missing data envelope for %s", name)
	}
	if err := validate("/components/schemas/"+escape(name), data); err != nil {
		t.Fatalf("%s response violates contract: %v", name, err)
	}
}

// Response also supports inline schemas, arrays and paginated responses. Path
// is the contract template (for example /products/{id}), not the concrete URL.
func Response(t testing.TB, method, path string, envelope map[string]any) {
	t.Helper()
	pointer := "/paths/" + escape(path) + "/" + strings.ToLower(method) + "/responses/200/content/application~1json/schema"
	if err := validate(pointer, envelope); err != nil {
		t.Fatalf("%s %s response violates contract: %v", method, path, err)
	}
}

func Error(t testing.TB, envelope map[string]any) {
	t.Helper()
	if err := validate("/components/schemas/Error", envelope); err != nil {
		t.Fatalf("error response violates contract: %v", err)
	}
	value, ok := envelope["error"].(map[string]any)
	if !ok || value["request_id"] == nil || value["request_id"] == "" {
		t.Fatal("missing error request_id")
	}
}
