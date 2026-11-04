import { ApiError } from './api';

/** User-facing message for an API failure; keeps machine codes out of the UI. */
export function describeError(e: unknown): string {
  if (!(e instanceof ApiError)) return 'Unexpected error. Please try again.';
  const extra = e.detail ? `: ${e.detail}` : '';
  switch (e.code) {
    case 'version_conflict':
      return 'The inventory was changed elsewhere. Reload and try again.';
    case 'git_conflict':
      return `Git conflict${e.paths.length ? ` in ${e.paths.join(', ')}` : ''}. Re-import or resolve upstream.`;
    case 'vault_requires_yaml':
      return `Vault values require a YAML inventory source${extra}`;
    case 'hostkey_not_pinned':
      return 'The SSH host key is not pinned yet. Scan and confirm it first.';
    case 'hostkey_mismatch':
      return 'The server host key does not match the pinned fingerprint.';
    case 'sync_in_progress':
      return 'A sync is already running for this project.';
    case 'project_not_synced':
      return 'The project has not been synced yet.';
  }
  return `${e.message}${extra}`;
}
