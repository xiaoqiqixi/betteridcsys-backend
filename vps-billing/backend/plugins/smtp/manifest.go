package smtp

import "aster/billing/pkg/pluginapi"

// Manifest is shared by the executable handshake and compatibility regression.
func Manifest(version, sha string) pluginapi.Manifest {
	return pluginapi.Manifest{Schema: 1, ID: "email.smtp", Version: version, Protocol: pluginapi.SMTPProtocol, CoreMin: "0.4.0", CoreMaxExclusive: "0.7.0", Capabilities: []string{pluginapi.SMTPCapability}, Executable: "plugin", SHA256: sha}
}
