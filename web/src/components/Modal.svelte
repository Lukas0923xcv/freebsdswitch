<script>
  import Icon from './Icon.svelte'
  let { title, onclose, wide = false, children, footer } = $props()
  function key(e) { if (e.key === 'Escape') onclose?.() }
</script>

<svelte:window onkeydown={key} />
<div class="backdrop" role="presentation" onclick={(e) => e.target === e.currentTarget && onclose?.()}>
  <div class="modal" class:wide role="dialog" aria-modal="true" aria-label={title}>
    <div class="head">
      <h2>{title}</h2>
      <button class="ghost icon" onclick={() => onclose?.()} aria-label="Close"><Icon name="x" /></button>
    </div>
    <div class="body">{@render children?.()}</div>
    {#if footer}<div class="foot">{@render footer()}</div>{/if}
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(10, 15, 22, .5); display: flex; align-items: flex-start; justify-content: center; z-index: 100; padding: 6vh 16px 16px; overflow-y: auto; }
  .modal { background: var(--surface); border: 1px solid var(--border); border-radius: 10px; box-shadow: var(--shadow-lg); width: 100%; max-width: 560px; }
  .modal.wide { max-width: 980px; }
  .head { display: flex; align-items: center; justify-content: space-between; padding: 14px 16px; border-bottom: 1px solid var(--border); }
  .body { padding: 16px; }
  .foot { padding: 12px 16px; border-top: 1px solid var(--border); display: flex; justify-content: flex-end; gap: 8px; flex-wrap: wrap; }
</style>
