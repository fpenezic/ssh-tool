/**
 * Which credential actually authenticates a connection, and whether that
 * combination is the key-plus-password fallback worth warning about.
 *
 * Split out of DetailPane so the rule is testable: the component reads it
 * through Svelte state that a unit test cannot construct.
 */

export type CredKind =
  | "password" | "key" | "agent" | "opkssh" | "vault" | "api_token";

/** Kinds that authenticate with a key/certificate rather than a password. */
export function isKeyAuthKind(kind: CredKind | undefined): boolean {
  return kind === "key" || kind === "agent" || kind === "opkssh";
}

/**
 * Resolve the credential id in effect: the connection's own reference when it
 * has one, otherwise the folder-inherited value.
 *
 * An empty string counts as "not set" - the editor stores unset references as
 * "" rather than null, so a `?? ` style check would treat that as a value and
 * skip inheritance entirely.
 */
export function effectiveAuthRef(
  ownRef: string | null | undefined,
  inheritedRef: string | null | undefined,
): string | null {
  if (ownRef) return ownRef;
  if (inheritedRef) return inheritedRef;
  return null;
}

/**
 * Whether to warn that a saved password is reachable as a fallback.
 *
 * The risk needs BOTH a key-ish credential (tried first) and a saved password
 * (sent if the server rejects the key). The credential may be inherited; the
 * password override is always per-connection.
 */
export function shouldWarnPasswordWithKey(
  authKind: CredKind | undefined,
  hasSavedPassword: boolean,
): boolean {
  return hasSavedPassword && isKeyAuthKind(authKind);
}

/**
 * Credentials offerable as SSH auth.
 *
 * api_token exists for the dynamic-inventory providers (Proxmox, Hetzner, ...)
 * and cannot authenticate an SSH session, so offering it only invites a
 * connection that fails at connect time. One already stored on the entity
 * stays listed, so opening an existing config does not silently drop it.
 */
export function isSelectableSSHCred(
  kind: CredKind,
  credId: string,
  currentRef: string | null | undefined,
): boolean {
  return kind !== "api_token" || credId === currentRef;
}
