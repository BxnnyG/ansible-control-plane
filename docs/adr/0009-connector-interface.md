# ADR-0009: Compiled-in connector interface with normalized output

- Status: accepted
- Date: 2026-11-03

## Context
OPNsense, Proxmox VE, NetBox and monitoring systems must feed one host/network model. Several instances per type are needed.

## Decision
- A Go interface `Connector { Type(); Test(ctx); Sync(ctx) (Result, error) }`; `Result` contains normalized `Network`, `HostObservation` and `Alert` records with source and timestamps.
- Connectors are compiled into the binary (no dynamic plugin loading); each type registers a config schema used by the UI and setup wizard.
- Sync runs in the app on an interval and on demand, with timeouts, backoff and per-instance status.
- Strictly read-only HTTP clients (GET only, enforced in a shared client wrapper), except the opt-in monitoring acknowledge.
- API details are verified against the installed versions; tests use recorded, sanitized fixtures.

## Consequences
New connectors require a release; in exchange there are no plugin ABI issues. The generic alert webhook covers systems without a connector.

## Alternatives
Go plugins / external processes via gRPC (more flexible, more operational complexity).
