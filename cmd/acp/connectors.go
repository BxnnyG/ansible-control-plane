package main

import (
	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/opnsense"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/proxmox"
)

// newConnectorRegistry returns the compiled-in connector types. Further
// connectors (NetBox, ...) are registered here as they are added.
func newConnectorRegistry() *connectors.Registry {
	reg := connectors.NewRegistry()
	opnsense.Register(reg)
	proxmox.Register(reg)
	return reg
}
