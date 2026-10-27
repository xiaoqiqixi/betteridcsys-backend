package controllers

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
)

// Policy is private host configuration, never a browser-editable URL or a
// product field. A credential authorizes only this fixed bridge endpoint.
type Policy struct {
	Schema      int       `json:"schema"`
	Controllers []Profile `json:"controllers"`
}

type Profile struct {
	ID           string   `json:"id"`
	Enabled      bool     `json:"enabled"`
	Endpoint     string   `json:"endpoint"`
	AllowedCIDRs []string `json:"allowed_cidrs"`
	TokenFile    string   `json:"token_file"`
	CAFile       string   `json:"ca_file"`
}

// ReadHostFile rejects links, shared-writable ancestors, non-root ownership,
// oversized files and group-readable secrets. It never includes file contents
// or paths in an error. Root-owned sticky temporary ancestors are allowed only
// above the independently checked private chain, not as a writable credential.
func ReadHostFile(path string, private bool, maximum int64) ([]byte, error) {
	return readHostFile(path, private, maximum, 0)
}

func readHostFile(path string, private bool, maximum int64, owner uint32) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") || maximum < 1 {
		return nil, ErrUnavailable
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrUnavailable
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 && st.Uid != owner || info.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return nil, ErrUnavailable
		}
		if parent == "/" {
			break
		}
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), "host-configuration")
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > maximum || before.Mode().Perm()&0022 != 0 || private && before.Mode().Perm()&0077 != 0 {
		return nil, ErrUnavailable
	}
	st, ok := before.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != owner || st.Nlink != 1 {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(f, maximum+1))
	after, statErr := f.Stat()
	if err != nil || statErr != nil || len(raw) == 0 || int64(len(raw)) > maximum || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, ErrUnavailable
	}
	return raw, nil
}

// strictJSON retains duplicate/null detection, unlike ordinary Unmarshal.
func strictJSON(raw []byte, target any) bool {
	if !utf8.Valid(raw) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 8 {
			return false
		}
		t, err := d.Token()
		if err != nil || t == nil {
			return false
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				k, ok := key.(string)
				if err != nil || !ok || seen[k] || !walk(depth+1) {
					return false
				}
				seen[k] = true
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case json.Delim('['):
			for d.More() {
				if !walk(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		}
		_, delim := t.(json.Delim)
		return !delim
	}
	if !walk(0) {
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return false
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(target) == nil
}

func (p Profile) endpoint() (*url.URL, []netip.Prefix, bool) {
	u, err := url.Parse(p.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || strings.ToLower(u.Host) != u.Host || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.ContainsAny(p.Endpoint, "\x00\r\n\\") || strings.Contains(u.Path, "..") || strings.Contains(u.Path, "//") {
		return nil, nil, false
	}
	port, portErr := strconv.Atoi(portFor(u))
	if portErr != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portFor(u) || len(p.AllowedCIDRs) < 1 || len(p.AllowedCIDRs) > 32 {
		return nil, nil, false
	}
	if u.Path == "" || u.Path == "/" {
		return nil, nil, false
	}
	for _, c := range u.Path {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/_.-", c)) {
			return nil, nil, false
		}
	}
	var prefixes []netip.Prefix
	for _, value := range p.AllowedCIDRs {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Masked().String() != value || prefix.Addr().Is4In6() || prefix.Bits() < 8 || prefix.Addr().Is6() && prefix.Bits() < 32 {
			return nil, nil, false
		}
		prefixes = append(prefixes, prefix)
	}
	return u, prefixes, true
}

func portFor(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	return "443"
}

func LoadPolicy(path string) (Policy, error) { return loadPolicy(path, 0) }

func loadPolicy(path string, owner uint32) (Policy, error) {
	raw, err := readHostFile(path, true, 64<<10, owner)
	var p Policy
	if err != nil || !strictJSON(raw, &p) || !exactKeys(raw, "schema", "controllers") || p.Schema != 1 || p.Controllers == nil || len(p.Controllers) > 64 {
		return Policy{}, ErrUnavailable
	}
	var encoded struct {
		Controllers []json.RawMessage `json:"controllers"`
	}
	if json.Unmarshal(raw, &encoded) != nil {
		return Policy{}, ErrUnavailable
	}
	seen := map[string]bool{}
	for n, profile := range p.Controllers {
		_, _, valid := profile.endpoint()
		if !exactKeys(encoded.Controllers[n], "id", "enabled", "endpoint", "allowed_cidrs", "token_file", "ca_file") || !controllerID.MatchString(profile.ID) || seen[profile.ID] || !valid || !filepath.IsAbs(profile.TokenFile) || filepath.Clean(profile.TokenFile) != profile.TokenFile || profile.CAFile != "" && (!filepath.IsAbs(profile.CAFile) || filepath.Clean(profile.CAFile) != profile.CAFile) {
			return Policy{}, ErrUnavailable
		}
		seen[profile.ID] = true
	}
	return p, nil
}

type Client struct {
	id       string
	endpoint *url.URL
	address  string
	tls      *tls.Config
	token    string
}

// Client resolves and pins one allowlisted destination before a database job
// is claimed. No environment proxy, redirects, plaintext or TLS downgrade.
func (p Policy) Client(ctx context.Context, id string) (*Client, error) {
	return p.client(ctx, id, 0, net.DefaultResolver.LookupNetIP)
}

func (p Policy) client(ctx context.Context, id string, owner uint32, lookup func(context.Context, string, string) ([]netip.Addr, error)) (*Client, error) {
	if p.Schema != 1 || !controllerID.MatchString(id) || len(p.Controllers) > 64 {
		return nil, ErrUnavailable
	}
	var selected *Profile
	seen := map[string]bool{}
	for _, profile := range p.Controllers {
		if seen[profile.ID] {
			return nil, ErrUnavailable
		}
		seen[profile.ID] = true
		if profile.ID == id && profile.Enabled {
			copy := profile
			selected = &copy
		}
	}
	if selected == nil {
		return nil, ErrUnavailable
	}

	token, err := readHostFile(selected.TokenFile, true, 4096, owner)
	if err != nil {
		return nil, ErrUnavailable
	}
	var ca []byte
	if selected.CAFile != "" {
		ca, err = readHostFile(selected.CAFile, false, 64<<10, owner)
		if err != nil {
			return nil, ErrUnavailable
		}
	}
	return credentialClient(ctx, *selected, token, ca, lookup)
}

// CredentialClient is for a separately approved resource worker's fixed,
// systemd-delivered credentials. It accepts no filesystem path and does not
// alter LoadPolicy/Client ownership rules. Callers must approve the profile
// outside any user-controlled request before calling this constructor.
func CredentialClient(ctx context.Context, profile Profile, token, ca []byte) (*Client, error) {
	if profile.TokenFile != "" || profile.CAFile != "" {
		return nil, ErrUnavailable
	}
	return credentialClient(ctx, profile, token, ca, net.DefaultResolver.LookupNetIP)
}
func credentialClient(ctx context.Context, selected Profile, token, ca []byte, lookup func(context.Context, string, string) ([]netip.Addr, error)) (*Client, error) {
	if ctx == nil || ctx.Err() != nil || !selected.Enabled || !controllerID.MatchString(selected.ID) || len(token) > 4096 || len(ca) > 64<<10 {
		return nil, ErrUnavailable
	}
	u, prefixes, valid := selected.endpoint()
	if !valid {
		return nil, ErrUnavailable
	}
	credential := strings.TrimSpace(string(token))
	if len(credential) < 16 || len(credential) > 4096 {
		return nil, ErrUnavailable
	}
	for _, c := range credential {
		if c < 33 || c > 126 {
			return nil, ErrUnavailable
		}
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	if len(ca) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, ErrUnavailable
		}
		tlsConfig.RootCAs = pool
	}
	addresses, err := lookup(ctx, "ip", u.Hostname())
	if err != nil || len(addresses) < 1 || len(addresses) > 16 {
		return nil, ErrUnavailable
	}
	for i, address := range addresses {
		address = address.Unmap()
		allowed := false
		for _, prefix := range prefixes {
			allowed = allowed || prefix.Contains(address)
		}
		if !address.IsValid() || address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || !allowed {
			return nil, ErrUnavailable
		}
		addresses[i] = address
	}
	sort.Slice(addresses, func(i, j int) bool { return addresses[i].Less(addresses[j]) })
	return &Client{id: selected.ID, endpoint: u, address: net.JoinHostPort(addresses[0].String(), portFor(u)), tls: tlsConfig, token: credential}, nil
}

func (c *Client) Send(ctx context.Context, request Request) (Result, error) {
	return c.call(ctx, request, false)
}

func (c *Client) Lookup(ctx context.Context, request Request) (Result, error) {
	return c.call(ctx, request, true)
}

func (c *Client) call(ctx context.Context, request Request, lookup bool) (Result, error) {
	if c == nil || c.endpoint == nil || c.tls == nil || c.token == "" {
		return Result{}, ErrUnavailable
	}
	if request.Product.ControllerID != c.id {
		return Result{}, ErrConfiguration
	}
	body, digest, err := request.BytesAndDigest()
	if err != nil {
		return Result{}, err
	}
	u := *c.endpoint
	method := http.MethodPost
	if lookup {
		method, body = http.MethodGet, nil
		u.Path = strings.TrimSuffix(u.Path, "/") + "/" + request.OperationID
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return Result{}, ErrConfiguration
	}
	req.GetBody = nil // Explicitly prohibit replaying a provision mutation.
	req.Close = true
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", request.OperationID)
	req.Header.Set("X-Aster-Request-SHA256", digest)
	var dials atomic.Int32
	dialer := net.Dialer{Timeout: 4 * time.Second, KeepAlive: -1}
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false,
		TLSNextProto:    map[string]func(string, *tls.Conn) http.RoundTripper{},
		TLSClientConfig: c.tls.Clone(), TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 7 * time.Second, MaxResponseHeaderBytes: 16 << 10,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			if !dials.CompareAndSwap(0, 1) {
				return nil, ErrUncertain
			}
			return dialer.DialContext(ctx, network, c.address)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return Result{}, ErrUncertain
	}
	defer response.Body.Close()
	media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || media != "application/json" || response.Header.Get("Content-Encoding") != "" || response.StatusCode != 200 && response.StatusCode != 201 && response.StatusCode != 202 && response.StatusCode != 409 {
		return Result{}, ErrUncertain
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	var result Result
	if err != nil || len(raw) > 16<<10 || !strictJSON(raw, &result) || !exactKeys(raw, "protocol", "operation_id", "order_id", "request_sha256", "status", "resource_id") || !result.Matches(request) {
		return Result{}, ErrUncertain
	}
	return result, nil
}

func exactKeys(raw []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}
