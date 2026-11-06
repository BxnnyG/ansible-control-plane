package main

import (
	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/alertwebhook"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/checkmk"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/netbox"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/opnsense"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/proxmox"
)

// newConnectorRegistry returns the compiled-in connector types. Further
// connectors are registered here as they are added.
func newConnectorRegistry() *connectors.Registry {
	reg := connectors.NewRegistry()
	opnsense.Register(reg)
	proxmox.Register(reg)
	netbox.Register(reg)
	checkmk.Register(reg)
	alertwebhook.Register(reg)
	return reg
}
