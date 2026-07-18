<script lang="ts">
  import type { Snippet } from "svelte";
  import { imageCache } from "./images.svelte";
  import { builtinIconByName } from "./builtinIcons";
  import { resolveColorTag } from "./palette";

  interface Props {
    imageId: string | null | undefined;
    // Built-in (lucide) icon: a name from builtinIcons + an optional
    // palette colour tinting it. Takes priority over the fallback but
    // NOT over an uploaded image (imageId wins - it's the more specific
    // choice). Both this and imageId absent -> fallback / emoji.
    iconName?: string | null;
    iconColor?: string | null;
    // Either a plain string (legacy emoji) or a snippet that renders
    // any fallback markup (lucide icon component, etc.). Snippet wins
    // if both are provided.
    fallback?: string;
    children?: Snippet;
    size?: number;
  }
  let {
    imageId,
    iconName = null,
    iconColor = null,
    fallback = "",
    children,
    size = 16,
  }: Props = $props();

  $effect(() => {
    imageCache.ensure(imageId);
  });

  const url = $derived(imageCache.peek(imageId));
  const builtin = $derived(builtinIconByName(iconName));
  const tint = $derived(resolveColorTag(iconColor));
</script>

{#if url}
  <img class="icon-img" src={url} alt="" style="width:{size}px;height:{size}px" />
{:else if builtin}
  {@const BI = builtin.icon}
  <span class="icon-builtin" style={tint ? `color:${tint}` : undefined}>
    <BI {size} />
  </span>
{:else if children}
  {@render children()}
{:else}
  <span class="icon-emoji">{fallback}</span>
{/if}

<style>
  .icon-img {
    display: inline-block;
    object-fit: contain;
    vertical-align: middle;
  }
  .icon-builtin {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    vertical-align: middle;
  }
  .icon-emoji {
    display: inline-block;
    font-size: 0.85rem;
    text-align: center;
  }
</style>
