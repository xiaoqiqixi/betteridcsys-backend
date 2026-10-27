package pluginapi

import "regexp"

var controllerIdentifier = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func ValidControllerID(id string) bool { return controllerIdentifier.MatchString(id) }
func ResourceCapabilities() []string   { return []string{"resource.send", "resource.lookup"} }

// Payload is canonical controllerapi JSON, base64 encoded by encoding/json.
// No credentials, money or paths are supplied over this boundary.
type ResourceRequest struct {
	Protocol     string `json:"protocol"`
	Version      string `json:"version"`
	RequestID    string `json:"request_id"`
	TimeoutMS    int64  `json:"timeout_ms"`
	ControllerID string `json:"controller_id"`
	Payload      []byte `json:"payload"`
}
type ResourceResponse struct {
	Protocol  string `json:"protocol"`
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Code      string `json:"code"`
	Payload   []byte `json:"payload"`
}
