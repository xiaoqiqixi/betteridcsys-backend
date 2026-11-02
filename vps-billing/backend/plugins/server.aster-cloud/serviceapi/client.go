package serviceapi

import (
	"aster/billing/plugins/server.aster-cloud/internal/controllers"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

type Profile = controllers.Profile
type Policy = controllers.Policy

var ErrUnavailable = controllers.ErrUnavailable
var ErrUncertain = controllers.ErrUncertain

type Client struct{ transport *controllers.Client }

func LoadPolicy(path string) (Policy, error) { return controllers.LoadPolicy(path) }

// New uses the approved controller's authority. Its provisioning endpoint path
// is replaced by fixed /v1 service routes; no API input selects a destination.
func New(ctx context.Context, policyPath, controllerID string) (*Client, error) {
	p, e := LoadPolicy(policyPath)
	if e != nil {
		return nil, e
	}
	c, e := p.Client(ctx, controllerID)
	if e != nil {
		return nil, e
	}
	return &Client{c}, nil
}
func NewCredentialTransport(ctx context.Context, p Profile, token, ca []byte) (*Client, error) {
	c, e := controllers.CredentialClient(ctx, p, token, ca)
	if e != nil {
		return nil, e
	}
	return &Client{c}, nil
}

// HTTPError contains only a validated status/code, never the response body.
// A transport or invalid response is ErrUncertain and must not trigger replay.
type HTTPError struct {
	StatusCode int
	Code       string
}

func (e *HTTPError) Error() string { return "service request rejected: " + e.Code }

var codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// Decode rejects duplicate keys, unknown fields, invalid UTF-8, extra JSON and
// excessive nesting. Nullable metrics remain null (never manufactured zero).
func Decode(raw []byte, target any) error {
	if len(raw) > 2<<20 || !utf8.Valid(raw) {
		return ErrConfiguration
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 16 {
			return false
		}
		t, e := d.Token()
		if e != nil {
			return false
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				key, ok := k.(string)
				if e != nil || !ok || seen[key] || !walk(depth+1) {
					return false
				}
				seen[key] = true
			}
			end, e := d.Token()
			return e == nil && end == json.Delim('}')
		case json.Delim('['):
			for d.More() {
				if !walk(depth + 1) {
					return false
				}
			}
			end, e := d.Token()
			return e == nil && end == json.Delim(']')
		}
		_, delim := t.(json.Delim)
		return !delim
	}
	if !walk(0) {
		return ErrConfiguration
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrConfiguration
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return ErrConfiguration
	}
	return nil
}

// required recursively verifies response shape, including false and zero fields.
func required(raw json.RawMessage, t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		if string(raw) == "null" {
			return true
		}
		return required(raw, t.Elem())
	}
	if t.PkgPath() == "time" {
		return string(raw) != "null"
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return false
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			v, ok := fields[name]
			if !ok || !required(v, f.Type) {
				return false
			}
		}
	case reflect.Slice:
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil || list == nil {
			return false
		}
		for _, v := range list {
			if !required(v, t.Elem()) {
				return false
			}
		}
	default:
		return string(raw) != "null"
	}
	return true
}
func (c *Client) call(ctx context.Context, method, path, query, owner string, body []byte, op, digest string, target any) (int, error) {
	if c == nil || c.transport == nil {
		return 0, ErrUnavailable
	}
	raw, status, e := c.transport.ServiceJSON(ctx, method, path, query, owner, body, op, digest)
	if e != nil {
		return 0, e
	}
	if status != 200 && status != 202 && !(status == 409 && op != "" && !bytes.Contains(raw, []byte(`"error"`))) {
		var result struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if status < 400 || status > 499 || Decode(raw, &result) != nil || !codePattern.MatchString(result.Error.Code) {
			return status, ErrUncertain
		}
		return status, &HTTPError{status, result.Error.Code}
	}
	if Decode(raw, target) != nil || !required(raw, reflect.TypeOf(target).Elem()) {
		return status, ErrUncertain
	}
	return status, nil
}
func servicePath(id string) (string, error) {
	if !ValidID(id) {
		return "", ErrConfiguration
	}
	return "/v1/services/" + id, nil
}
func (c *Client) Images(ctx context.Context, owner string) ([]Image, error) {
	var v []Image
	status, e := c.call(ctx, "GET", "/v1/images", "", owner, nil, "", "", &v)
	if e != nil {
		return nil, e
	}
	if status != 200 {
		return nil, ErrUncertain
	}
	seen := map[string]bool{}
	for _, im := range v {
		if !im.Valid() || seen[im.ID] {
			return nil, ErrUncertain
		}
		seen[im.ID] = true
	}
	return v, nil
}
func (c *Client) Service(ctx context.Context, id, owner string) (Service, error) {
	var v Service
	p, e := servicePath(id)
	if e != nil {
		return v, e
	}
	status, e := c.call(ctx, "GET", p, "", owner, nil, "", "", &v)
	if e != nil {
		return Service{}, e
	}
	powers := map[string]bool{"running": true, "stopped": true, "starting": true, "stopping": true, "rebooting": true, "reinstalling": true, "unknown": true}
	if status != 200 || v.Protocol != Protocol || v.ResourceID != id || v.OwnerRef != owner || !powers[v.Power] {
		return Service{}, ErrUncertain
	}
	return v, nil
}

// Action returns the immutable receipt and whether HTTP 409 replayed it. A
// conflicting digest returns HTTPError; never retry ErrUncertain automatically.
func (c *Client) Action(ctx context.Context, id string, r ActionRequest) (ActionResult, bool, error) {
	var v ActionResult
	p, e := servicePath(id)
	if e != nil {
		return v, false, e
	}
	b, h, e := r.BytesAndDigest()
	if e != nil || r.Action == "terminate" && r.Params.Confirmation != id {
		return v, false, ErrConfiguration
	}
	status, e := c.call(ctx, "POST", p+"/actions", "", r.OwnerRef, b, r.OperationID, h, &v)
	if e != nil {
		return ActionResult{}, false, e
	}
	if (status != 202 && status != 409) || v.OperationID != r.OperationID || (v.Status != "accepted" && v.Status != "rejected") || !codePattern.MatchString(v.Code) {
		return ActionResult{}, false, ErrUncertain
	}
	return v, status == 409, nil
}
func (c *Client) Operation(ctx context.Context, id, owner, op string) (Operation, error) {
	var v Operation
	p, e := servicePath(id)
	if e != nil || !ValidID(op) {
		return v, ErrConfiguration
	}
	status, e := c.call(ctx, "GET", p+"/actions/"+op, "", owner, nil, "", "", &v)
	if e != nil {
		return Operation{}, e
	}
	states := map[string]bool{"accepted": true, "running": true, "succeeded": true, "failed": true, "uncertain": true}
	if status != 200 || v.OperationID != op || !states[v.Status] || !codePattern.MatchString(v.Code) {
		return Operation{}, ErrUncertain
	}
	return v, nil
}
func (c *Client) Console(ctx context.Context, id, owner string) (Console, error) {
	var v Console
	p, e := servicePath(id)
	if e != nil {
		return v, e
	}
	status, e := c.call(ctx, "POST", p+"/console", "", owner, nil, "", "", &v)
	if e != nil {
		return Console{}, e
	}
	u, err := url.Parse(v.URL)
	if status != 200 || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "/console/" || u.RawQuery != "" || u.Fragment == "" || v.ExpiresAt.IsZero() {
		return Console{}, ErrUncertain
	}
	return v, nil
}
func (c *Client) Metrics(ctx context.Context, id, owner, period string) (Metrics, error) {
	var v Metrics
	p, e := servicePath(id)
	if e != nil || !ValidRange(period) {
		return v, ErrConfiguration
	}
	status, e := c.call(ctx, "GET", p+"/metrics", "range="+period, owner, nil, "", "", &v)
	if e != nil {
		return Metrics{}, e
	}
	if status != 200 || v.StepSeconds < 1 {
		return Metrics{}, ErrUncertain
	}
	return v, nil
}
func (c *Client) Snapshots(ctx context.Context, id, owner string) (Snapshots, error) {
	var v Snapshots
	p, e := servicePath(id)
	if e != nil {
		return v, e
	}
	status, e := c.call(ctx, "GET", p+"/snapshots", "", owner, nil, "", "", &v)
	if e != nil {
		return Snapshots{}, e
	}
	if status != 200 || !v.RequiresStopped || v.Limit < 0 {
		return Snapshots{}, ErrUncertain
	}
	return v, nil
}
