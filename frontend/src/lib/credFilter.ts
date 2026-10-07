// Credential search: what the filter box above the credentials tree matches.
// Pure, so it is testable without the stores module.

export interface CredSearchable {
  name: string;
  kind: string;
  hint?: string | null;
  default_username?: string | null;
  tags?: string[] | null;
  config?: Record<string, unknown> | null;
}

// Every whitespace-separated word of the query must appear (case-
// insensitively) in the credential's name, default username, hint, tags,
// kind, external backend (keepass / bitwarden / infisical) or the name of a
// folder it sits in. "root prod" finds the root password in Prod.
export function credMatches(c: CredSearchable, query: string, folderNames: string[] = []): boolean {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const backend = credBackend(c);
  const hay = [
    c.name, c.default_username ?? "", c.hint ?? "", c.kind, backend,
    ...(c.tags ?? []), ...folderNames,
  ].join("\n").toLowerCase();
  return words.every((w) => hay.includes(w));
}

// The external secret backend a credential reads from, or "".
export function credBackend(c: Pick<CredSearchable, "config">): string {
  const cfg = c.config ?? {};
  return cfg.keepass_ref ? "keepass" : cfg.bitwarden_ref ? "bitwarden" : cfg.infisical_ref ? "infisical" : "";
}

// What the tree shows on the right of a credential row: the backend for
// an external one, else the kind.
export function credKindLabel(c: Pick<CredSearchable, "kind" | "config">): string {
  return credBackend(c) || c.kind;
}
