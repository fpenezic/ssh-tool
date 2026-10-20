// Which service cards (Database, Kafka) of the Services tab are folded.
// Per viewer, like the last editor tab: a bastion with many databases
// folds the Database card once and finds Kafka under it from then on.

const KEY = "ssh-tool.editor.folded";

function load(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? "[]");
    return Array.isArray(v) ? v.filter((x) => typeof x === "string") : [];
  } catch {
    return [];
  }
}

export const serviceCards = $state({ folded: load() });

export function toggleCard(id: string) {
  serviceCards.folded = serviceCards.folded.includes(id)
    ? serviceCards.folded.filter((x) => x !== id)
    : [...serviceCards.folded, id];
  try {
    localStorage.setItem(KEY, JSON.stringify(serviceCards.folded));
  } catch {
    /* private window */
  }
}
