<script>
  import { get, post, patch, del } from '../lib/api.js'
  import { app, isAdmin, toast, errorToast, refreshStatus, bump } from '../lib/store.svelte.js'

  let data = $state({ entries: [], aging: 0, max: 0, total: 0 })
  let statics = $state([])
  let cand = $state(null)
  let q = $state('')
  let vlan = $state('')
  let port = $state('')
  let aging = $state(300)
  let nm = $state({ mac: '', vlan: 1, port: '' })

  async function load() {
    try {
      ;[data, statics, cand] = await Promise.all([get('/mac-table'), get('/static-macs'), get('/config/candidate')])
      aging = cand.bridge.mac_aging
      if (!nm.port && cand.ports.length) nm.port = cand.ports[0].name
    } catch (e) { errorToast(e) }
  }
  $effect(() => { app.version; load() })
  $effect(() => { const t = setInterval(async () => { try { data = await get('/mac-table') } catch {} }, 5000); return () => clearInterval(t) })

  const norm = (s) => s.toLowerCase().replace(/[-.]/g, ':')
  const rows = $derived(data.entries.filter((e) =>
    (!q || e.mac.includes(norm(q))) && (!vlan || e.vlan === Number(vlan)) && (!port || e.port === port)))
  const portsInTable = $derived([...new Set(data.entries.map((e) => e.port))].sort())
  const vlansInTable = $derived([...new Set(data.entries.map((e) => e.vlan))].sort((a, b) => a - b))

  async function flush() {
    if (!confirm('Flush all dynamically learned MAC addresses?')) return
    try { await post('/mac-table/flush', {}); toast('MAC table flushed', 'ok'); load() } catch (e) { errorToast(e) }
  }
  async function saveAging() {
    try { await patch('/bridge', { mac_aging: Number(aging) }); toast('Aging time staged', 'ok'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }
  async function addStatic(e) {
    e.preventDefault()
    if (!/^([0-9a-f]{2}[:-]){5}[0-9a-f]{2}$/i.test(nm.mac)) { toast('MAC must look like 00:11:22:33:44:55', 'bad'); return }
    try { await post('/static-macs', { mac: nm.mac, vlan: Number(nm.vlan), port: nm.port }); nm.mac = ''; toast('Static entry staged', 'ok'); await refreshStatus(); bump() } catch (err) { errorToast(err) }
  }
  async function removeStatic(s) {
    try { await del(`/static-macs/${s.vlan}/${s.mac}`); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }
</script>

<div class="page-head">
  <div><h1>MAC address table</h1><p>{data.total} entries (limit {data.max}), aging {data.aging}s. Learning is per VLAN.</p></div>
  {#if isAdmin()}<button class="danger" onclick={flush}>Flush dynamic entries</button>{/if}
</div>

<div class="grid" style="grid-template-columns:minmax(0,2fr) minmax(0,1fr)">
  <div class="card">
    <div class="card-head">
      <div class="row">
        <input placeholder="Search MAC…" bind:value={q} style="width:200px" aria-label="search MAC" />
        <select bind:value={vlan} style="width:auto" aria-label="VLAN filter"><option value="">All VLANs</option>{#each vlansInTable as v}<option value={v}>VLAN {v}</option>{/each}</select>
        <select bind:value={port} style="width:auto" aria-label="port filter"><option value="">All ports</option>{#each portsInTable as p}<option value={p}>{p}</option>{/each}</select>
      </div>
      <span class="small muted">{rows.length} shown</span>
    </div>
    <div class="card-body flush table-wrap" style="max-height:65vh">
      <table>
        <thead><tr><th>VLAN</th><th>MAC address</th><th>Port</th><th>Type</th><th class="num">Expires in</th></tr></thead>
        <tbody>
          {#each rows as e (e.vlan + e.mac)}
            <tr><td>{e.vlan}</td><td class="mono">{e.mac}</td><td>{e.port}</td>
              <td>{#if e.static}<span class="badge info">static</span>{:else if e.sticky}<span class="badge">sticky</span>{:else}<span class="badge">dynamic</span>{/if}</td>
              <td class="num">{e.static ? '—' : e.expire + 's'}</td></tr>
          {:else}
            <tr><td colspan="5" class="empty">No entries</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>
  <div class="stack">
    <div class="card">
      <div class="card-head"><h2>Aging time</h2></div>
      <div class="card-body row">
        <input type="number" min="10" max="1000000" bind:value={aging} style="width:120px" disabled={!isAdmin()} aria-label="aging seconds" /> seconds
        {#if isAdmin()}<button onclick={saveAging}>Stage</button>{/if}
      </div>
    </div>
    <div class="card">
      <div class="card-head"><h2>Static entries</h2></div>
      <div class="card-body stack">
        {#each statics as s}
          <div class="row between"><span class="mono">{s.mac}</span><span class="small">VLAN {s.vlan} · {s.port}</span>{#if isAdmin()}<button class="sm danger" onclick={() => removeStatic(s)}>Remove</button>{/if}</div>
        {:else}<div class="faint small">No static entries.</div>{/each}
        {#if isAdmin() && cand}
          <form class="stack" onsubmit={addStatic}>
            <hr style="margin:4px 0" />
            <div class="field"><label for="sm">MAC</label><input id="sm" bind:value={nm.mac} placeholder="00:11:22:33:44:55" /></div>
            <div class="row">
              <div class="field grow"><label for="sv">VLAN</label><select id="sv" bind:value={nm.vlan}>{#each cand.vlans as v}<option value={v.id}>{v.id}</option>{/each}</select></div>
              <div class="field grow"><label for="sp">Port</label><select id="sp" bind:value={nm.port}>{#each cand.ports as p}<option value={p.name}>{p.name}</option>{/each}</select></div>
            </div>
            <button type="submit">Add static entry</button>
          </form>
        {/if}
      </div>
    </div>
  </div>
</div>
