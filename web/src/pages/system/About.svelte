<script>
  import { get } from '../../lib/api.js'
  let caps = $state([])
  let sys = $state(null)
  get('/capabilities').then((c) => (caps = c))
  get('/system').then((s) => (sys = s))
  const cls = { supported: 'ok', partial: 'warn', unsupported: 'bad', planned: 'info' }
</script>

<div class="stack" style="gap:16px">
  {#if sys}<div class="card"><div class="card-body"><b>bsdswitch {sys.version}</b> <span class="muted">({sys.commit}) on FreeBSD {sys.os_release} {sys.machine}</span></div></div>{/if}
  <div class="card">
    <div class="card-head"><h2>Features compared with a hardware smart switch</h2></div>
    <div class="card-body flush table-wrap">
      <table>
        <thead><tr><th>Feature</th><th>Status</th><th>Notes</th></tr></thead>
        <tbody>{#each caps as c}<tr><td>{c.feature}</td><td><span class="badge {cls[c.status]}">{c.status}</span></td><td class="small muted">{c.notes}</td></tr>{/each}</tbody>
      </table>
    </div>
  </div>
</div>
