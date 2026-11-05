<script>
  let { text = '' } = $props()
  const lines = $derived(text.split('\n').filter((l, i, a) => i < a.length - 1 || l !== ''))
  function cls(l) {
    if (l.startsWith('@@')) return 'hunk'
    if (l.startsWith('+') && !l.startsWith('+++')) return 'add'
    if (l.startsWith('-') && !l.startsWith('---')) return 'del'
    return ''
  }
</script>

{#if text}
  <div class="diff">{#each lines as l}<div class={cls(l)}>{l || ' '}</div>{/each}</div>
{:else}
  <div class="empty">No differences.</div>
{/if}
