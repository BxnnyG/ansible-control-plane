// Package alertwebhook registers the "alert_webhook" connector type (ADR-0019).
// It has no remote system to pull from: alerts are pushed to
// POST /api/hooks/alerts/{connector_id} and handled by the connector service
// (connectors.Service.HandleAlertWebhook). Sync therefore returns an empty
// result that does not touch stored alerts.
package alertwebhook

import (
	"context"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// Type is the registered connector type name.
const Type = connectors.AlertWebhookType

// Spec returns the registry spec of the type.
func Spec() connectors.Spec {
	return connectors.Spec{
		Type: Type, Label: "Alert webhook",
		SecretTypes: []string{secrets.TypeToken}, SecretRequired: false, GeneratesSecret: true,
		Help: "Receives alerts at POST /api/hooks/alerts/<connector id> with \"Authorization: Bearer <secret>\". " +
			"The secret is generated on create and shown once (or select an existing token secret). Accepts the " +
			"ACP JSON format and the Alertmanager webhook payload (version 4); see the connector documentation.",
	}
}

// Register adds the type to reg.
func Register(reg *connectors.Registry) { reg.Register(Spec(), New) }

type conn struct{}

// New builds the connector; it needs no credentials of its own.
func New(connectors.Instance, connectors.Credential) (connectors.Connector, error) {
	return conn{}, nil
}

func (conn) Type() string { return Type }

// Test always succeeds: there is nothing to reach.
func (conn) Test(context.Context) error { return nil }

// Sync returns an empty result; ReplaceAlerts stays false so pushed alerts are
// never resolved by the sync loop.
func (conn) Sync(context.Context) (connectors.Result, error) { return connectors.Result{}, nil }
