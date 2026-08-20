<script lang="ts">
  // Word-style grid picker: hover a cell to choose columns x rows, click to
  // confirm. Shown when merging several tabs into one, where the useful
  // question is "what shape?" rather than "yes/no" - a fixed square guess is
  // wrong as often as it is right (6 sessions may want 6x1, 3x2 or 2x3).
  //
  // Only shapes that can hold every pane are offered, and the highlighted
  // cells show exactly which slots get filled, so an arrangement that would
  // leave holes is visible before committing.
  interface Props {
    count: number;
    onPick: (cols: number, rows: number) => void;
    onCancel: () => void;
  }
  let { count, onPick, onCancel }: Props = $props();

  // Every column count from 1..count is a valid arrangement (the balanced
  // split always fits), so the grid only ever shows reachable shapes - there
  // are no disabled cells to explain. Capped so a 20-session merge does not
  // render a 20-wide matrix.
  const MAX = 6;
  // Column counts worth offering. A layout whose tallest column exceeds this
  // is not a usable terminal grid - 17 panes in one column is a 40px-tall
  // sliver each - and rendered as an unreadable stripe in the preview. The
  // floor keeps at least three choices even for large merges.
  const MAX_ROWS = 8;
  const options = $derived.by(() => {
    const all = Array.from({ length: Math.min(count, MAX) }, (_, i) => i + 1);
    const usable = all.filter((c) => Math.max(...columnSizes(c)) <= MAX_ROWS);
    return usable.length >= 1 ? usable : all.slice(-3);
  });

  // Start on the squarest shape that fits - the same default the plain
  // "merge" action used, so hovering nothing and hitting Enter is sane.
  // Squarest arrangement, snapped to an offered option.
  const defCols = $derived.by(() => {
    const want = Math.ceil(Math.sqrt(count));
    const opts = options;
    return opts.includes(want) ? want : (opts.find((c) => c >= want) ?? opts[opts.length - 1]);
  });
  // Panes per column, mirroring mergeTabsIntoGrid's balanced split: the
  // remainder goes to the LEADING columns, so the preview shows the shape that
  // will actually be built rather than an idealised rectangle.
  function columnSizes(cols: number): number[] {
    const base = Math.floor(count / cols);
    const extra = count % cols;
    return Array.from({ length: cols }, (_, i) => base + (i < extra ? 1 : 0));
  }

  // Clicking a cell LOCKS that layout; the pointer can then leave the grid
  // without the shape drifting (hover-only selection kept changing the choice
  // on the way to the button). Merge without picking anything uses the
  // squarest arrangement, so the dialog is one click for the common case and
  // two when a specific shape is wanted.
  let lockedCols = $state(0);
  let hoverCols = $state(0);
  const selCols = $derived(hoverCols || lockedCols || defCols);
  // Rows follow from the column count: the tallest column is what the grid
  // ends up being, so showing anything else would misrepresent the result.

  // Columns that hold fewer panes than the tallest one stretch to full height,
  // because the pane tree has no empty cells: 5 panes in 3 columns renders as
  // 2 + 2 + one full-height pane, not a 3x2 grid with a hole. Say so.
  const uneven = $derived(new Set(columnSizes(selCols)).size > 1);
</script>

<div class="backdrop" role="presentation" onclick={onCancel}></div>
<div class="picker" role="dialog" aria-label="Choose grid layout">
  <div class="head">Arrange {count} tabs</div>
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="grid" onmouseleave={() => (hoverCols = 0)}>
    {#each options as c}
      {@const inShape = c <= selCols}
      <!-- Each column is a fixed, self-contained preview of "what if I pick N
           columns" - its cell count comes from `c`, never from the current
           selection. Deriving it from the selection made every column reflow
           as the pointer moved, so cells slid out from under the cursor and
           the rightmost column was almost impossible to land on. -->
      {@const ownSizes = columnSizes(c)}
      {@const ownTallest = Math.max(...ownSizes)}
      <button
        class="col"
        class:locked={lockedCols === c && hoverCols === 0}
        class:sel={inShape}
        onmouseenter={() => (hoverCols = c)}
        onfocus={() => (hoverCols = c)}
        onclick={() => (lockedCols = c)}
        aria-label="{c} column{c === 1 ? '' : 's'}"
        title="{c} column{c === 1 ? '' : 's'}"
      >
        <span class="mini">
          {#each ownSizes as size}
            <span class="mini-col">
              {#each Array(size) as _}
                <span class="cell" class:on={inShape} style="flex: {ownTallest / size}"></span>
              {/each}
            </span>
          {/each}
        </span>
      </button>
    {/each}
  </div>
  <!-- Fixed two-line box: the "uneven" note appears and disappears as the
       pointer moves across columns, and letting it reflow made the whole
       dialog jump under the cursor mid-hover. -->
  <div class="label">
    <div>{selCols} column{selCols === 1 ? "" : "s"}{lockedCols && !hoverCols ? " (selected)" : ""}</div>
    <div class="note">{uneven ? "uneven - short columns stretch" : ""}</div>
  </div>
  <div class="actions">
    <button class="btn" onclick={onCancel}>Cancel</button>
    <button class="btn primary" onclick={() => onPick(selCols, Math.max(...columnSizes(selCols)))}>
      Merge
    </button>
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; z-index: 60; }
  .picker {
    position: fixed;
    z-index: 61;
    top: 50%; left: 50%;
    transform: translate(-50%, -50%);
    /* Sized for the widest grid it can show (MAX columns), so switching
       between layouts never resizes or recentres the dialog. */
    box-sizing: border-box;
    background: var(--base);
    border: 1px solid var(--surface1);
    border-radius: 6px;
    padding: 0.7rem;
    box-shadow: 0 8px 28px rgba(0, 0, 0, 0.45);
  }
  .head { font-size: 0.8rem; color: var(--subtext0); margin-bottom: 0.5rem; }
  /* One tile per option (1..MAX columns), each showing that whole layout.
     Fixed tile size means nothing moves as the pointer travels. */
  .grid {
    display: flex;
    gap: 6px;
    justify-content: center;
    flex-wrap: wrap;
    max-width: 17rem;
  }
  .col {
    padding: 3px;
    background: var(--surface0);
    border: 1px solid var(--surface1);
    border-radius: 3px;
    cursor: pointer;
    width: 3.6rem;
    height: 3rem;
  }
  .col:hover { border-color: var(--overlay0); }
  .col.sel { border-color: var(--blue); }
  .col.locked { box-shadow: 0 0 0 1px var(--text); }
  .mini {
    display: flex;
    gap: 2px;
    width: 100%;
    height: 100%;
  }
  .mini-col {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1;
  }
  .cell {
    display: block;
    background: var(--surface1);
    border-radius: 1px;
    /* No min-height: with many rows per column a minimum forces overflow and
       the cells collapse to nothing. flex alone divides the tile exactly. */
    min-height: 0;
  }
  .cell.on { background: var(--blue); }


  .label {
    margin-top: 0.5rem;
    font-size: 0.78rem;
    text-align: center;
    /* Two lines' worth, always - see the comment on the markup. */
    height: 2.4em;
  }
  .note { color: var(--subtext0); font-size: 0.72rem; }
  .actions { display: flex; gap: 0.4rem; justify-content: flex-end; margin-top: 0.6rem; }
  .btn {
    background: var(--surface0);
    border: 1px solid var(--surface1);
    color: var(--text);
    border-radius: 4px;
    padding: 0.25rem 0.6rem;
    font-size: 0.78rem;
    cursor: pointer;
  }
  .btn.primary { background: var(--blue); border-color: var(--blue); color: var(--on-accent); }
</style>
