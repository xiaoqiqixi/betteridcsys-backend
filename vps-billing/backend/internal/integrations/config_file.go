package integrations

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// LoadConfigFile reads a local deployment-controlled JSON object mapping provider
// IDs to configuration fields. It never interpolates values or logs contents.
// Registry construction applies installed-provider schemas and secret-ref rules.
func LoadConfigFile(path string) (map[string]Config, error) {
	if path == "" {
		return nil, nil
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("integration configuration file unavailable")
	}
	defer f.Close()
	return DecodeConfigs(f)
}
func DecodeConfigs(reader io.Reader) (map[string]Config, error) {
	b, e := io.ReadAll(io.LimitReader(reader, 65537))
	if e != nil || len(b) > 65536 {
		return nil, errors.New("integration configuration exceeds read limit")
	}
	// Token-by-token decoding rejects duplicate provider/field keys rather than
	// silently choosing the last value in a sensitive deployment configuration.
	d := json.NewDecoder(bytes.NewReader(b))
	start, e := d.Token()
	if e != nil || start != json.Delim('{') {
		return nil, errors.New("integration configuration must be an object")
	}
	out := map[string]Config{}
	for d.More() {
		token, e := d.Token()
		if e != nil {
			return nil, errors.New("invalid integration configuration")
		}
		id, ok := token.(string)
		if !ok || !identifier.MatchString(id) {
			return nil, errors.New("invalid integration ID")
		}
		if _, ok := out[id]; ok {
			return nil, errors.New("duplicate integration configuration")
		}
		start, e = d.Token()
		if e != nil || start != json.Delim('{') {
			return nil, errors.New("provider configuration must be an object")
		}
		cfg := Config{}
		for d.More() {
			token, e = d.Token()
			if e != nil {
				return nil, errors.New("invalid provider configuration")
			}
			key, ok := token.(string)
			if !ok {
				return nil, errors.New("invalid configuration key")
			}
			if _, ok := cfg[key]; ok {
				return nil, errors.New("duplicate configuration field")
			}
			token, e = d.Token()
			value, ok := token.(string)
			if e != nil || !ok {
				return nil, errors.New("configuration values must be strings")
			}
			cfg[key] = value
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return nil, errors.New("invalid provider configuration")
		}
		out[id] = cfg
	}
	end, e := d.Token()
	if e != nil || end != json.Delim('}') {
		return nil, errors.New("invalid integration configuration")
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, errors.New("trailing integration configuration")
	}
	return out, nil
}
