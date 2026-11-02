package serverv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"unicode/utf8"
)

type FieldText struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}
type Field struct {
	Type        string               `json:"type"`
	Title       string               `json:"title,omitempty"`
	Description string               `json:"description,omitempty"`
	I18n        map[string]FieldText `json:"x-i18n,omitempty"`
	Enum        []any                `json:"enum,omitempty"`
	Items       *Field               `json:"items,omitempty"`
	Minimum     *float64             `json:"minimum,omitempty"`
	Maximum     *float64             `json:"maximum,omitempty"`
	MinLength   *int                 `json:"minLength,omitempty"`
	MaxLength   *int                 `json:"maxLength,omitempty"`
	MinItems    *int                 `json:"minItems,omitempty"`
	MaxItems    *int                 `json:"maxItems,omitempty"`
	UniqueItems bool                 `json:"uniqueItems,omitempty"`
	Default     any                  `json:"default,omitempty"`
	WriteOnly   bool                 `json:"writeOnly,omitempty"`
}
type Schema struct {
	Type                 string           `json:"type"`
	Properties           map[string]Field `json:"properties"`
	Required             []string         `json:"required,omitempty"`
	AdditionalProperties bool             `json:"additionalProperties"`
}

func EmptySchema() Schema { return Schema{Type: "object", Properties: map[string]Field{}} }

var fieldName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

func (s Schema) Validate() error {
	if s.Type != "object" || s.AdditionalProperties || s.Properties == nil || len(s.Properties) > 64 {
		return ErrConfiguration
	}
	for key, f := range s.Properties {
		if !fieldName.MatchString(key) || len(f.Enum) > 100 || len(f.Title) > 200 || len(f.Description) > 2000 {
			return ErrConfiguration
		}
		switch f.Type {
		case "string", "integer", "number", "boolean":
			if f.Items != nil {
				return ErrConfiguration
			}
		case "array":
			if f.Items == nil || len(f.Items.Enum) == 0 || f.Items.Type == "array" || f.Items.Items != nil {
				return ErrConfiguration
			}
		default:
			return ErrConfiguration
		}
		if f.Minimum != nil && f.Maximum != nil && *f.Minimum > *f.Maximum {
			return ErrConfiguration
		}
		if f.WriteOnly && (f.Type != "string" || f.Default != nil) {
			return ErrConfiguration
		}
		for _, e := range f.Enum {
			if _, err := normalizeScalar(f.Type, e); err != nil {
				return err
			}
		}
		if f.Items != nil {
			for _, e := range f.Items.Enum {
				if _, err := normalizeScalar(f.Items.Type, e); err != nil {
					return err
				}
			}
		}
		if f.Default != nil {
			if _, err := f.normalize(f.Default); err != nil {
				return err
			}
		}
	}
	seen := map[string]bool{}
	for _, key := range s.Required {
		if _, ok := s.Properties[key]; !ok || seen[key] {
			return ErrConfiguration
		}
		seen[key] = true
	}
	return nil
}
func normalizeScalar(kind string, v any) (any, error) {
	switch kind {
	case "string":
		s, ok := v.(string)
		if ok && utf8.ValidString(s) && len([]rune(s)) <= 4000 {
			return s, nil
		}
	case "boolean":
		b, ok := v.(bool)
		if ok {
			return b, nil
		}
	case "number", "integer":
		var n float64
		switch x := v.(type) {
		case json.Number:
			var err error
			n, err = x.Float64()
			if err != nil {
				return nil, ErrConfiguration
			}
		case float64:
			n = x
		case int:
			n = float64(x)
		case int64:
			n = float64(x)
		default:
			return nil, ErrConfiguration
		}
		if !math.IsNaN(n) && !math.IsInf(n, 0) && math.Abs(n) <= 9007199254740991 && (kind != "integer" || math.Trunc(n) == n) {
			return n, nil
		}
	}
	return nil, ErrConfiguration
}
func equal(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func (f Field) normalize(v any) (any, error) {
	if f.Type == "array" {
		a, ok := v.([]any)
		if !ok || f.Items == nil || len(a) > 100 || f.MinItems != nil && len(a) < *f.MinItems || f.MaxItems != nil && len(a) > *f.MaxItems {
			return nil, ErrConfiguration
		}
		out := []any{}
		for _, x := range a {
			n, e := f.Items.normalize(x)
			if e != nil {
				return nil, e
			}
			for _, p := range out {
				if equal(n, p) {
					return nil, ErrConfiguration
				}
			}
			out = append(out, n)
		}
		return out, nil
	}
	n, e := normalizeScalar(f.Type, v)
	if e != nil {
		return nil, e
	}
	if f.Enum != nil {
		found := false
		for _, x := range f.Enum {
			if equal(n, x) {
				found = true
			}
		}
		if !found {
			return nil, ErrConfiguration
		}
	}
	switch x := n.(type) {
	case string:
		l := len([]rune(x))
		if f.MinLength != nil && l < *f.MinLength || f.MaxLength != nil && l > *f.MaxLength {
			return nil, ErrConfiguration
		}
	case float64:
		if f.Minimum != nil && x < *f.Minimum || f.Maximum != nil && x > *f.Maximum {
			return nil, ErrConfiguration
		}
	}
	return n, nil
}
func (s Schema) Normalize(values Fields) (Fields, string, error) {
	if s.Validate() != nil {
		return nil, "", ErrConfiguration
	}
	out := Fields{}
	for key, v := range values {
		f, ok := s.Properties[key]
		if !ok {
			return nil, "", ErrConfiguration
		}
		n, e := f.normalize(v)
		if e != nil {
			return nil, "", e
		}
		out[key] = n
	}
	for key, f := range s.Properties {
		if _, ok := out[key]; !ok && f.Default != nil {
			n, e := f.normalize(f.Default)
			if e != nil {
				return nil, "", e
			}
			out[key] = n
		}
	}
	for _, key := range s.Required {
		v, ok := out[key]
		if !ok || v == "" {
			return nil, "", ErrConfiguration
		}
	}
	b, e := json.Marshal(out)
	if e != nil {
		return nil, "", ErrConfiguration
	}
	h := sha256.Sum256(b)
	return out, hex.EncodeToString(h[:]), nil
}
func (s Schema) Split(values Fields) (Fields, Fields) {
	public, secret := Fields{}, Fields{}
	for k, v := range values {
		if s.Properties[k].WriteOnly {
			secret[k] = v
		} else {
			public[k] = v
		}
	}
	return public, secret
}
