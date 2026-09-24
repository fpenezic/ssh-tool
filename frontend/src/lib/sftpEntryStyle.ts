// Row styling for the SFTP pane: which visual classes an entry gets.
// Pure so it can be unit tested; the pane maps the result onto CSS
// classes and the view toggles decide which of them are switched on.

export type EntryKind = "arch" | "code" | "cap" | "cfg" | "";

export interface EntryStyle {
  hidden: boolean;
  temp: boolean;
  // "partial" for an interrupted transfer (.part), "temp" for editor and
  // browser leftovers.
  tempLabel: "partial" | "temp" | "";
  kind: EntryKind;
  foreign: boolean;
}

const TEMP_RE = /(\.part|\.partial|\.tmp|\.swp|\.swo|\.crdownload|\.download|~)$/i;
const ARCH_RE = /\.(zip|rar|7z|tar|gz|tgz|xz|txz|bz2|tbz2|zst|lz4|deb|rpm|apk|iso|img)$/i;
const CODE_RE = /\.(sh|bash|zsh|fish|py|js|mjs|ts|go|rb|pl|php|lua|ps1|bat|cmd)$/i;
const CAP_RE = /\.(pcap|pcapng|cap)$/i;
const CFG_RE = /\.(conf|cfg|cnf|ini|toml|ya?ml|json|env|service|timer|socket|rules)$/i;
const CFG_DOT_RE = /^\.(bashrc|bash_profile|profile|zshrc|vimrc|gitconfig|inputrc|tmux\.conf|screenrc)$/;

export function entryStyle(
  e: { name: string; is_dir: boolean; owner?: string },
  loginUser: string,
): EntryStyle {
  const temp = !e.is_dir && TEMP_RE.test(e.name);
  let kind: EntryKind = "";
  if (!e.is_dir && !temp) {
    if (ARCH_RE.test(e.name)) kind = "arch";
    else if (CODE_RE.test(e.name)) kind = "code";
    else if (CAP_RE.test(e.name)) kind = "cap";
    else if (CFG_RE.test(e.name) || CFG_DOT_RE.test(e.name)) kind = "cfg";
  }
  return {
    hidden: e.name.startsWith("."),
    temp,
    tempLabel: !temp ? "" : /\.(part|partial)$/i.test(e.name) ? "partial" : "temp",
    kind,
    // Only a resolved name is compared: a bare uid could be the login
    // user's own on a host without a passwd entry for it.
    foreign: !!loginUser && !!e.owner && e.owner !== loginUser,
  };
}
