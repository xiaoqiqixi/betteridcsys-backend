package main

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"aster/billing/pkg/pluginserver"
	servertemplate "aster/billing/plugins/server-template"
	"os"
)

func main() {
	if pluginserver.RunServer(pluginserver.ServerDefinition{ID: servertemplate.ID, Version: "1.0.0", Capabilities: pluginapi.ServerCapabilities()}, func(pluginserver.ServerOptions) (serverv1.Processor, error) { return servertemplate.New(), nil }) != nil {
		os.Exit(1)
	}
}
