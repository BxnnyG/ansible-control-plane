# Security Policy

## Supported versions

| Version | Supported |
| --- | --- |
| 1.0.x | yes |
| pre-release milestones (m0 to m7) | no |

## Reporting a vulnerability

Do not open a public issue. Use GitHub private vulnerability reporting
("Security" tab, "Report a vulnerability") of this repository. Include the
affected version, reproduction steps and the impact. Never include real secrets
or production data in the report.

You can expect an acknowledgement within a few days and a fix or mitigation
plan after triage. Fixes are released as patch versions with a note in
[`CHANGELOG.md`](CHANGELOG.md); reporters are credited on request.

## Scope

In scope: the app, worker, CLI, compose files and scripts in this repository.
Out of scope: vulnerabilities in third-party components without a flaw in how
this project uses them, and findings that require a malicious operator with
host or master key access. The security model and its assumptions are described
in [`docs/operations.md`](docs/operations.md#security-model).
