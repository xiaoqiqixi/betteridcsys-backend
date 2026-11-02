package controllers

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"strings"
)

// ServiceJSON shares the fixed approved authority and single-dial transport.
// Its path is built solely by the service SDK, never from a caller-supplied URL.
func (c *Client) ServiceJSON(ctx context.Context, method, path, query, owner string, body []byte, operation, digest string) ([]byte, int, error) {
	if c == nil || c.endpoint == nil || c.tls == nil || c.token == "" {
		return nil, 0, ErrUnavailable
	}
	if !operationID.MatchString(owner) || (method != "GET" && method != "POST") || !serviceRoute(method, path, query) {
		return nil, 0, ErrConfiguration
	}
	u := *c.endpoint
	u.Path = path
	u.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, 0, ErrConfiguration
	}
	req.GetBody = nil
	req.Close = true
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Aster-Owner-Ref", owner)
	if operation != "" {
		req.Header.Set("Idempotency-Key", operation)
		req.Header.Set("X-Aster-Request-SHA256", digest)
	}
	response, err := c.do(req)
	if err != nil {
		return nil, 0, ErrUncertain
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || response.Header.Get("Content-Encoding") != "" {
		return nil, 0, ErrUncertain
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return nil, 0, ErrUncertain
	}
	return raw, response.StatusCode, nil
}
func serviceRoute(method, path, query string) bool {
	if path == "/v1/images" {
		return method == "GET" && query == ""
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/services/"), "/")
	if !strings.HasPrefix(path, "/v1/services/") || len(parts) < 1 || !operationID.MatchString(parts[0]) {
		return false
	}
	if len(parts) == 1 {
		return method == "GET" && query == ""
	}
	if len(parts) == 3 {
		return method == "GET" && parts[1] == "actions" && operationID.MatchString(parts[2]) && query == ""
	}
	if len(parts) != 2 {
		return false
	}
	switch parts[1] {
	case "actions", "console":
		return method == "POST" && query == ""
	case "snapshots":
		return method == "GET" && query == ""
	case "metrics":
		return method == "GET" && (query == "range=1h" || query == "range=24h" || query == "range=7d" || query == "range=30d")
	}
	return false
}
