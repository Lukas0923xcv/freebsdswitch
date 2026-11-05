<script>
  import { app } from '../lib/store.svelte.js'
</script>

<div class="toasts" aria-live="polite">
  {#each app.toasts as t (t.id)}
    <div class="toast {t.kind}" role="status">{t.msg}</div>
  {/each}
</div>

<style>
  .toasts { position: fixed; right: 16px; bottom: 16px; display: flex; flex-direction: column; gap: 8px; z-index: 200; max-width: min(440px, calc(100vw - 32px)); }
  .toast { padding: 10px 14px; border-radius: 8px; background: var(--surface); border: 1px solid var(--border); box-shadow: var(--shadow-lg); border-left: 4px solid var(--accent); overflow-wrap: anywhere; }
  .toast.ok { border-left-color: var(--ok); }
  .toast.warn { border-left-color: var(--warn); }
  .toast.bad { border-left-color: var(--bad); }
</style>
