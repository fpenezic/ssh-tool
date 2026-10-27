<script lang="ts">
  // An on/off switch for a setting that turns a feature on and opens its
  // own settings below (App proxy, Liveness probe, Session sharing). It
  // sits left of the section title. Plain options inside a section stay
  // checkboxes. Same look as the VNC / Kafka / Database switches.
  interface Props {
    checked: boolean;
    onchange: (on: boolean) => void;
    label: string;
    disabled?: boolean;
    title?: string;
  }
  let { checked, onchange, label, disabled = false, title }: Props = $props();
</script>

<button
  type="button"
  class="switch"
  class:on={checked}
  role="switch"
  aria-checked={checked}
  aria-label={label}
  {title}
  {disabled}
  onclick={() => onchange(!checked)}
><span class="knob"></span></button>

<style>
  .switch {
    position: relative;
    width: 34px;
    height: 18px;
    border-radius: 9px;
    border: 1px solid var(--border);
    background: var(--surface2);
    padding: 0;
    cursor: pointer;
    flex: 0 0 auto;
    transition: background 0.15s ease, border-color 0.15s ease;
  }
  .switch.on {
    background: var(--accent, #4a8);
    border-color: var(--accent, #4a8);
  }
  .switch:disabled { opacity: 0.5; cursor: default; }
  .knob {
    position: absolute;
    top: 1px;
    left: 1px;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    background: var(--text1);
    transition: transform 0.15s ease;
  }
  .switch.on .knob {
    transform: translateX(16px);
    background: #fff;
  }
</style>
