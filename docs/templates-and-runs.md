# Templates and runs (REST API)

Implements the user-facing part of ADR-0016 (RUN-1, RUN-2, RUN-5, RUN-7). All routes live under
`/api/v1/workspaces/{ws}/` and declare their permission through the central policy package.

| Route | Permission |
|-------|------------|
| `GET templates`, `GET templates/{id}` | `template.read` |
| `POST templates`, `PUT/DELETE templates/{id}` | `template.write` (unscoped memberships only) |
| `POST templates/{id}/runs` | `run.execute` |
| `GET runs` (filters: `template`, `status`, `trigger`, `user`, `host`, `cursor`, `limit`), `GET runs/{id}` | `run.read` |
| `POST runs/{id}/cancel` | `run.cancel` |

## Templates

A template references a project (with a playbook from the project's last scan), an inventory, optional
SSH/become secrets (by id, never by value) and an ordered list of vault identities
(`vault_identity_ids`). Create validates that everything belongs to the workspace and that the playbook
exists in the latest scanned revision. Update is a partial `PUT`; `null` clears nullable references.
Deleting a template cancels its queued runs in the same transaction; run history is kept.

`allow_*_override` flags decide which per-run overrides are accepted (limit, tags/skip_tags, check, diff,
verbosity, extra vars). Without `allow_vars_override`, only variables defined by the template survey may
be set at run time. The survey is a list of fields (shape of `scan.Field`) or an object with `fields`;
without a template survey the `argument_specs` of the roles used by the playbook type-check supplied
variables (required fields are not enforced there).

## Starting a run

`POST templates/{id}/runs` takes optional overrides (`limit`, `tags`, `skip_tags`, `extra_vars`, `check`,
`diff`, `verbosity`). The service merges them with the template defaults, validates the survey, pins the
project's latest synced revision, resolves the limit against the inventory (Ansible pattern subset: names,
groups, wildcards, `:`/`,` union, `:&` intersection, `:!` exclusion) and authorizes the effective host set
with `Authorizer.AuthorizeHosts` (403 when outside the caller's scope, 422 when the limit matches no
host). The run row stores the full parameter snapshot (`runs.params`) so later template edits do not
affect queued runs. Values of survey fields flagged `secret` are masked in every API response.

Scoped memberships only see runs whose whole host set lies in their scope (others are 404).
