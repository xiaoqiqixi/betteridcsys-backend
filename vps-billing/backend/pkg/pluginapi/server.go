package pluginapi

import "regexp"

var connectionIdentifier = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func ValidConnectionID(id string) bool { return connectionIdentifier.MatchString(id) }
func ServerCapabilities() []string     { return []string{"server.call"} }

type ServerRequest struct {
	Protocol     string `json:"protocol"`
	Version      string `json:"version"`
	RequestID    string `json:"request_id"`
	TimeoutMS    int64  `json:"timeout_ms"`
	ConnectionID string `json:"connection_id"`
	Payload      []byte `json:"payload"`
}
type ServerResponse struct {
	Protocol  string `json:"protocol"`
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Code      string `json:"code"`
	Payload   []byte `json:"payload"`
}
