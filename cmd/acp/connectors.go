package main

import (
	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/opnsense"
)

// newConnectorRegistry returns the compiled-in connector types. Further
// connectors (Proxmox, ...) are registered here as they are added.
func newConnectorRegistry() *connectors.Registry {
	reg := connectors.NewRegistry()
	opnsense.Register(reg)
	return reg
}
