<script>
  import { get } from '../../lib/api.js'
  import { app, errorToast } from '../../lib/store.svelte.js'
  import { time } from '../../lib/format.js'
  let rows = $state([])
  $effect(() => { app.version; get('/audit?n=500').then((r) => (rows = r)).catch(errorToast) })
</script>

<div class="card">
  <div class="card-head"><h2>Audit log</h2><span class="small muted">who changed what and when</span></div>
  <div class="card-body flush table-wrap" style="max-height:70vh">
    <table>
      <thead><tr><th>Time</th><th>User</th><th>Source</th><th>Action</th><th>Result</th><th>Detail</th></tr></thead>
      <tbody>
        {#each rows as a}
          <tr><td class="small nowrap">{time(a.time)}</td><td>{a.user}</td><td class="small mono">{a.remote || ''}</td><td>{a.action}</td>
            <td>{#if a.ok}<span class="badge ok">ok</span>{:else}<span class="badge bad">failed</span>{/if}</td><td class="small">{a.detail}</td></tr>
        {:else}<tr><td colspan="6" class="empty">No entries</td></tr>{/each}
      </tbody>
    </table>
  </div>
</div>
