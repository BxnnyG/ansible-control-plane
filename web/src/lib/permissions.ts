/** Permission names as declared by the backend policy package (UI hint only; the server is authoritative). */
export type Permission = 'workspace.read' | 'workspace.manage' | 'workspace.delete' | (string & {});

export function can(permissions: readonly string[], permission: Permission): boolean {
  return permissions.includes(permission);
}
