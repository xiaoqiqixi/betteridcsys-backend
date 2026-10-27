package updates

import "net/http"

// This factory is compiled only into the test variant of the package. The
// production core has no transport, URL, or environment override for updates.
func NewForTransportTest(token string, current func() Current, transport http.RoundTripper) *Service {
	service := New(token, current)
	service.client.http.Transport = transport
	return service
}

// Fixed synthetic official responses for full HTTP/API integration. Program
// and resource endpoints are separately marked forbidden instead of serving
// executable bytes. This helper never exists in the production package.
func DistributionHTTPFixtureForTest(component, format string) (map[string][]byte, map[string]bool) {
	fixture := newFixture(component)
	if format == RawDistribution {
		fixture = newRawFixture(component)
	}
	payloads := map[string][]byte{}
	for path, raw := range fixture.values {
		payloads[path] = raw
	}
	forbidden := map[string]bool{}
	for _, value := range fixture.release["assets"].([]any) {
		asset := value.(map[string]any)
		name := asset["name"].(string)
		path := "/repos/" + Repository + "/releases/assets/" + stringNumber(asset["id"].(int64))
		if name == "BUILD.json" || name == "PROVENANCE.json" || name == "SHA256SUMS" {
			payloads[path] = fixture.assets[path]
		} else {
			forbidden[path] = true
		}
	}
	return payloads, forbidden
}
