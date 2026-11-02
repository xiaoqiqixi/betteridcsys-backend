package main

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"aster/billing/pkg/pluginserver"
	astercloud "aster/billing/plugins/server.aster-cloud"
	"context"
	"os"
)

func main() {
	err := pluginserver.RunServer(pluginserver.ServerDefinition{ID: astercloud.ID, Version: astercloud.Version, Capabilities: pluginapi.ServerCapabilities()}, func(o pluginserver.ServerOptions) (serverv1.Processor, error) {
		raw, err := pluginserver.ReadServerCredential("server_config.json")
		if err != nil {
			return nil, err
		}
		cfg, err := astercloud.DecodeConfig(raw, o.Targets)
		clear(raw)
		if err != nil {
			return nil, err
		}
		token, err := pluginserver.ReadServerCredential("server_token")
		if err != nil {
			return nil, err
		}
		defer clear(token)
		var ca []byte
		if cfg.CustomCA {
			ca, err = pluginserver.ReadServerCredential("server_ca.pem")
			if err != nil {
				return nil, err
			}
			defer clear(ca)
		}
		return astercloud.Open(context.Background(), cfg, token, ca, "/var/lib/aster-server/"+o.ConnectionID)
	})
	if err != nil {
		os.Exit(1)
	}
}
