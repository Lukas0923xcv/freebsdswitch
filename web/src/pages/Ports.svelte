<script>
  import { get, patch, post } from '../lib/api.js'
  import { app, isAdmin, toast, errorToast, refreshStatus, bump, navigate } from '../lib/store.svelte.js'
  import { bps, speed, vlanList, count } from '../lib/format.js'
  import PortEditor from '../components/PortEditor.svelte'
  import PortGraph from '../components/PortGraph.svelte'
  import Modal from '../components/Modal.svelte'

  let ports = $state([])
  let cand = $state(null)
  let vlans = $state([])
  let selected = $state(new Set())
  let editing = $state(null)
  let graphing = $state(null)
  let bulk = $state(null)
  let filter = $state('')

  async function load() {
    try {
      ;[ports, cand, vlans] = await Promise.all([get('/ports'), get('/config/candidate'), get('/vlans?source=candidate')])
      const e = app.query.get('edit')
      if (e && !editing) openEdit(e)
    } catch (e) { errorToast(e) }
  }
  $effect(() => { app.version; app.linkVersion; load() })
  $effect(() => { const t = setInterval(async () => { try { ports = await get('/ports') } catch {} }, 5000); return () => clearInterval(t) })

  function openEdit(name) {
    const p = ports.find((x) => x.name === name)
    const c = cand?.ports?.find((x) => x.name === name)
    if (p && c) editing = { port: p, config: c }
  }
  function closeEdit() { editing = null; if (app.query.get('edit')) navigate('/ports', true) }

  const shown = $derived(ports.filter((p) => !filter || (p.name + ' ' + p.description).toLowerCase().includes(filter.toLowerCase())))
  const allSel = $derived(shown.length > 0 && shown.every((p) => selected.has(p.name)))
  function toggle(name) { const s = new Set(selected); s.has(name) ? s.delete(name) : s.add(name); selected = s }
  function toggleAll() { selected = allSel ? new Set() : new Set(shown.map((p) => p.name)) }

  function candPort(name) { return cand?.ports?.find((x) => x.name === name) }
  function linkState(p) {
    if (!p.exists) return ['missing', 'bad']
    if (p.shutdown || !p.admin_up) return ['disabled', '']
    return p.link_up ? ['up', 'ok'] : ['down', '']
  }
  function stpBadge(p) {
    const s = p.stp.state
    if (!p.stp.enabled) return ['off', '']
    if (s === 'forwarding') return [p.stp.role || 'fwd', 'ok']
    if (s === 'discarding' || s === 'blocking') return [s, 'warn']
    return [s || '—', '']
  }

  let bulkMode = $state('pvid')
  let bulkVlan = $state(1)
  async function applyBulk() {
    const set = bulkMode === 'pvid' ? { mode: 'access', pvid: Number(bulkVlan) } : bulkMode === 'enable' ? { shutdown: false } : { shutdown: true }
    try {
      await patch('/ports', { names: [...selected], set })
      toast(`${selected.size} ports updated (staged)`, 'ok')
      bulk = null; selected = new Set()
      await refreshStatus(); bump()
    } catch (e) { errorToast(e) }
  }
  async function resetAll() {
    if (!confirm('Reset the counters of all ports?')) return
    try { await post('/ports/reset-counters'); toast('Counters reset', 'ok'); load() } catch (e) { errorToast(e) }
  }
</script>

<div class="page-head">
  <div><h1>Ports</h1><p>Status and settings of all switch ports. Edits are staged until you apply them.</p></div>
  <div class="row">
    <input placeholder="Filter…" bind:value={filter} style="width:180px" aria-label="filter ports" />
    {#if isAdmin()}
      <button onclick={resetAll}>Reset counters</button>
      <button class="primary" disabled={!selected.size} onclick={() => (bulk = true)} data-testid="bulk-edit">Bulk edit ({selected.size})</button>
    {/if}
  </div>
</div>

<div class="card">
  <div class="card-body flush table-wrap">
    <table data-testid="ports-table">
      <thead>
        <tr>
          {#if isAdmin()}<th><input type="checkbox" checked={allSel} onchange={toggleAll} aria-label="select all" /></th>{/if}
          <th>Port</th><th>Description</th><th>Link</th><th>Speed</th><th>MTU</th><th>Mode</th><th>VLAN</th><th>Tagged</th><th>STP</th>
          <th class="num">RX</th><th class="num">TX</th><th class="num">Errors</th><th></th>
        </tr>
      </thead>
      <tbody>
        {#each shown as p (p.name)}
          {@const [ls, lc] = linkState(p)}
          {@const [ss, sc] = stpBadge(p)}
          {@const live = app.live?.ports?.[p.name] || p.rate}
          {@const c = candPort(p.name)}
          <tr class:selected={selected.has(p.name)} data-port={p.name}>
            {#if isAdmin()}<td><input type="checkbox" checked={selected.has(p.name)} onchange={() => toggle(p.name)} aria-label="select {p.name}" /></td>{/if}
            <td class="nowrap"><b>{p.name}</b>{#if p.staged}<span class="badge info" title="staged changes">staged</span>{/if}{#if p.is_lag}<span class="badge">LAG</span>{/if}</td>
            <td class="muted">{p.description}</td>
            <td><span class="badge {lc}">{ls}</span></td>
            <td class="nowrap">{speed(p.speed)}{p.duplex !== 'n/a' ? ' ' + p.duplex : ''}</td>
            <td>{p.mtu || '—'}</td>
            <td>{c?.mode || p.mode}</td>
            <td>{(c?.mode || p.mode) === 'trunk' ? (c?.native ? 'native ' + c.native : 'none') : (c?.pvid ?? p.pvid)}</td>
            <td class="mono small">{vlanList(c?.mode === 'trunk' ? c?.tagged : []) || '—'}</td>
            <td><span class="badge {sc}">{ss}</span></td>
            <td class="num nowrap">{bps(live.rx_bps)}</td>
            <td class="num nowrap">{bps(live.tx_bps)}</td>
            <td class="num" class:bad={p.counters.rx_errors + p.counters.tx_errors > 0}>{count(p.counters.rx_errors + p.counters.tx_errors)}</td>
            <td class="nowrap right">
              <button class="sm" onclick={() => (graphing = p)}>Graph</button>
              <button class="sm" onclick={() => openEdit(p.name)} data-testid="edit-{p.name}">{isAdmin() ? 'Edit' : 'View'}</button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>
<p class="small faint">"n/a": the NIC (e.g. virtio) has no settable speed/duplex. VLAN columns show the staged configuration; STP and link show live state.</p>

{#if editing}<PortEditor port={editing.port} config={editing.config} {vlans} onclose={closeEdit} />{/if}
{#if graphing}<PortGraph port={graphing} onclose={() => (graphing = null)} />{/if}
{#if bulk}
  <Modal title="Bulk edit {selected.size} ports" onclose={() => (bulk = null)}>
    <div class="stack">
      <div class="small muted">{[...selected].join(', ')}</div>
      <div class="field"><label for="bm">Action</label>
        <select id="bm" bind:value={bulkMode}><option value="pvid">Set access VLAN</option><option value="enable">Enable ports</option><option value="disable">Disable ports</option></select></div>
      {#if bulkMode === 'pvid'}
        <div class="field"><label for="bv">VLAN</label><select id="bv" bind:value={bulkVlan}>{#each vlans as v}<option value={v.id}>{v.id} – {v.name}</option>{/each}</select>
          <div class="hint">Trunk ports in the selection become access ports.</div></div>
      {/if}
    </div>
    {#snippet footer()}<button onclick={() => (bulk = null)}>Cancel</button><button class="primary" onclick={applyBulk}>Stage</button>{/snippet}
  </Modal>
{/if}

<style>
  td.bad { color: var(--bad); font-weight: 600; }
</style>
