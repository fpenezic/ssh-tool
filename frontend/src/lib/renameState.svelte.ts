// Which tree row is currently being renamed in place.
//
// The tree is recursive (TreeNode renders TreeNode), and the F2 key is
// handled up in Sidebar where the selection lives, so the row that has to
// turn into an input is not the component that saw the key. A tiny shared
// store is the connection between the two: Sidebar sets a target, and
// whichever TreeNode owns that id swaps its label for a field.
//
// Only one row renames at a time, so this is a single value rather than a
// set.

type Target = { kind: "connection" | "folder"; id: string } | null;

class RenameState {
  private target = $state<Target>(null);

  /** begin puts a row into rename mode. */
  begin(kind: "connection" | "folder", id: string) {
    this.target = { kind, id };
  }

  /** editing reports whether this specific row is the one being renamed. */
  editing(kind: "connection" | "folder", id: string): boolean {
    return this.target?.kind === kind && this.target?.id === id;
  }

  /** end leaves rename mode, whether the edit was saved or abandoned. */
  end() {
    this.target = null;
  }

  active(): boolean {
    return this.target !== null;
  }
}

export const renameState = new RenameState();
