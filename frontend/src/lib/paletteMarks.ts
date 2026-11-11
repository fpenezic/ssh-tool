// What Enter opens in the quick palette while hosts are marked: the marked
// set, plus the host under the cursor when it is not marked yet. Marking two
// hosts with Ctrl+Enter, searching a third and pressing Enter opens all
// three; the third used to be left out.

export interface MarkLike {
  id: string;
}

export function enterSet<M extends MarkLike>(marks: M[], current: M | null): M[] {
  if (!current || marks.some((m) => m.id === current.id)) return [...marks];
  return [...marks, current];
}
