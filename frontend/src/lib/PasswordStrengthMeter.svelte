<script lang="ts">
  // 5-segment strength bar + label + optional feedback list. Reactive
  // to the bound `password` prop so callers just pass the current
  // input value.

  import { estimateStrength } from "./passwordStrength";

  interface Props {
    password: string;
    showFeedback?: boolean;
  }
  let { password, showFeedback = true }: Props = $props();

  const result = $derived(estimateStrength(password));

  const COLORS = ["var(--red)", "var(--peach)", "var(--yellow)", "var(--green)", "var(--sapphire)"];
</script>

<div class="meter" class:empty={!password}>
  <div class="bar">
    {#each [0, 1, 2, 3, 4] as i (i)}
      <span
        class="seg"
        style:background={i <= result.score && password ? COLORS[result.score] : "var(--surface0)"}
      ></span>
    {/each}
  </div>
  <div class="row">
    <span class="label" style:color={password ? COLORS[result.score] : "var(--overlay0)"}>
      {result.label}
    </span>
    {#if password}
      <span class="entropy">~{Math.round(result.entropy)} bits</span>
    {/if}
  </div>
  {#if showFeedback && result.feedback.length > 0 && password}
    <ul class="feedback">
      {#each result.feedback as f (f)}
        <li>{f}</li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .meter {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    font-size: 0.75rem;
    margin-top: 0.3rem;
  }
  .bar { display: flex; gap: 2px; height: 4px; }
  .seg {
    flex: 1;
    border-radius: 2px;
    transition: background 0.2s ease;
  }
  .row {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    color: var(--subtext0);
  }
  .label { font-weight: 600; font-size: 0.75rem; }
  .entropy { color: var(--overlay0); font-size: 0.7rem; }
  .feedback {
    margin: 0.15rem 0 0;
    padding-left: 1rem;
    color: var(--overlay1);
    font-size: 0.7rem;
    line-height: 1.4;
  }
  .feedback li { margin: 0; }
</style>
