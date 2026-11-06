package main

import "github.com/BxnnyG/ansible-control-plane/internal/connectors"

// newConnectorRegistry returns the compiled-in connector types. Concrete
// connectors (OPNsense, Proxmox, ...) are registered here as they are added.
func newConnectorRegistry() *connectors.Registry {
	return connectors.NewRegistry()
}
