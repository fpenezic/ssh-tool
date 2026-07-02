<script lang="ts">
  import type { Snippet } from "svelte";
  import { imageCache } from "./images.svelte";

  interface Props {
    imageId: string | null | undefined;
    // Either a plain string (legacy emoji) or a snippet that renders
    // any fallback markup (lucide icon component, etc.). Snippet wins
    // if both are provided.
    fallback?: string;
    children?: Snippet;
    size?: number;
  }
  let { imageId, fallback = "", children, size = 16 }: Props = $props();

  $effect(() => {
    imageCache.ensure(imageId);
  });

  const url = $derived(imageCache.peek(imageId));
</script>

{#if url}
  <img class="icon-img" src={url} alt="" style="width:{size}px;height:{size}px" />
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
  .icon-emoji {
    display: inline-block;
    font-size: 0.85rem;
    text-align: center;
  }
</style>
