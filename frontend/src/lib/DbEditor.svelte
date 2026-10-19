<script lang="ts">
  // SQL editor of a query tab: CodeMirror 6 with the engine's dialect and
  // completion of the table names the tree has listed. Ctrl+Enter runs the
  // statement under the cursor (or the selection), Ctrl+Shift+Enter the
  // whole script. The text is two-way bound; the parent keeps it in the
  // tab state.

  import { onDestroy, onMount, untrack } from "svelte";
  import { EditorState, Compartment } from "@codemirror/state";
  import { EditorView, keymap, lineNumbers, highlightActiveLine, drawSelection, placeholder as cmPlaceholder } from "@codemirror/view";
  import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
  import { syntaxHighlighting, defaultHighlightStyle, bracketMatching, HighlightStyle } from "@codemirror/language";
  import { autocompletion, completionKeymap, closeBrackets, closeBracketsKeymap } from "@codemirror/autocomplete";
  import { sql, PostgreSQL, MySQL } from "@codemirror/lang-sql";
  import { tags } from "@lezer/highlight";

  interface Props {
    value: string;
    engine: string;
    // Table names for completion ("schema.table" and "table").
    tables: string[];
    onRun: (mode: "statement" | "all") => void;
  }
  let { value = $bindable(), engine, tables, onRun }: Props = $props();

  let host: HTMLDivElement;
  let view: EditorView | null = null;
  const lang = new Compartment();

  // Colours from the app theme, so light and dark both read.
  const highlight = HighlightStyle.define([
    { tag: tags.keyword, color: "var(--mauve)" },
    { tag: [tags.string, tags.special(tags.string)], color: "var(--green)" },
    { tag: [tags.number, tags.bool, tags.null], color: "var(--peach)" },
    { tag: tags.comment, color: "var(--overlay1)", fontStyle: "italic" },
    { tag: [tags.typeName, tags.standard(tags.name)], color: "var(--yellow)" },
    { tag: tags.operator, color: "var(--sky)" },
    { tag: [tags.special(tags.name), tags.quote], color: "var(--teal)" },
  ]);
  const theme = EditorView.theme({
    "&": { height: "100%", fontSize: "0.82rem", backgroundColor: "var(--mantle)", color: "var(--text)" },
    ".cm-scroller": { fontFamily: "var(--mono-font, ui-monospace, monospace)", overflow: "auto" },
    ".cm-content": { caretColor: "var(--text)" },
    ".cm-gutters": { backgroundColor: "var(--mantle)", color: "var(--overlay0)", border: "none" },
    ".cm-activeLine": { backgroundColor: "color-mix(in srgb, var(--surface0) 45%, transparent)" },
    ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--subtext0)" },
    "&.cm-focused .cm-selectionBackground, .cm-selectionBackground": { backgroundColor: "color-mix(in srgb, var(--blue) 30%, transparent) !important" },
    "&.cm-focused": { outline: "none" },
    ".cm-tooltip": { backgroundColor: "var(--surface0)", color: "var(--text)", border: "1px solid var(--surface1)" },
    ".cm-tooltip-autocomplete > ul > li[aria-selected]": { backgroundColor: "var(--surface2)", color: "var(--text)" },
    ".cm-placeholder": { color: "var(--overlay0)" },
  });

  function sqlFor(eng: string, names: string[]) {
    const schema: Record<string, string[]> = {};
    for (const n of names) schema[n] = [];
    return sql({ dialect: eng === "mysql" ? MySQL : PostgreSQL, schema, upperCaseKeywords: true });
  }

  onMount(() => {
    view = new EditorView({
      parent: host,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          history(),
          drawSelection(),
          highlightActiveLine(),
          bracketMatching(),
          closeBrackets(),
          autocompletion(),
          syntaxHighlighting(highlight),
          syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
          cmPlaceholder("SELECT ...   Ctrl+Enter runs the statement under the cursor, Ctrl+Shift+Enter the whole script"),
          lang.of(sqlFor(engine, tables)),
          keymap.of([
            { key: "Mod-Enter", run: () => { onRun("statement"); return true; } },
            { key: "Mod-Shift-Enter", run: () => { onRun("all"); return true; } },
            ...closeBracketsKeymap,
            ...defaultKeymap,
            ...historyKeymap,
            ...completionKeymap,
            indentWithTab,
          ]),
          theme,
          EditorView.updateListener.of((u) => {
            if (u.docChanged) value = u.state.doc.toString();
          }),
        ],
      }),
    });
  });

  // Text changed from outside (tab state adopted, "Query" from the tree).
  $effect(() => {
    const v = value;
    untrack(() => {
      if (view && view.state.doc.toString() !== v) {
        view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: v } });
      }
    });
  });
  $effect(() => {
    const eng = engine;
    const names = tables;
    untrack(() => view?.dispatch({ effects: lang.reconfigure(sqlFor(eng, names)) }));
  });

  onDestroy(() => {
    view?.destroy();
    view = null;
  });

  // The selection when there is one, else null; and the cursor position.
  export function selection(): { text: string | null; cursor: number } {
    if (!view) return { text: null, cursor: 0 };
    const sel = view.state.selection.main;
    return { text: sel.empty ? null : view.state.sliceDoc(sel.from, sel.to), cursor: sel.head };
  }
  export function focus() {
    view?.focus();
  }
  // Briefly marks the range that ran.
  export function flash(from: number, to: number) {
    if (!view || to <= from) return;
    view.dispatch({ selection: { anchor: from, head: to } });
  }
</script>

<div class="db-editor" bind:this={host}></div>

<style>
  .db-editor { height: 100%; min-height: 0; overflow: hidden; }
</style>
