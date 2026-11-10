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
    border-radius: var(--r-pill);
    border: 1px solid var(--surface2);
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
    /* White in both states (see Settings' switch cards): a knob in the
       text colour made "off" a dark blob in Latte. */
    background: #fff;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.25);
    transition: transform 0.15s ease;
  }
  .switch.on .knob {
    transform: translateX(16px);
  }
  /* Windows high contrast (forced colors): system colours, so the knob
     stays visible against the track and "on" reads as selected. */
  @media (forced-colors: active) {
    .switch { border-color: ButtonText; background: ButtonFace; forced-color-adjust: none; }
    .switch.on { background: Highlight; border-color: Highlight; }
    .knob { background: ButtonText; forced-color-adjust: none; }
    .switch.on .knob { background: HighlightText; }
  }
</style>
