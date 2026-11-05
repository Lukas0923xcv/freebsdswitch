<script>
  import { get, post, patch, del } from '../lib/api.js'
  import { app, isAdmin, toast, errorToast, refreshStatus, bump } from '../lib/store.svelte.js'
  import Modal from '../components/Modal.svelte'

  let tab = $state(app.query.get('tab') || 'matrix')
  let cand = $state(null)
  let run = $state(null)
  let vlans = $state([])
  let newId = $state('')
  let newName = $state('')
  let renaming = $state(null)
  let busyCell = $state('')

  async function load() {
    try {
      ;[cand, run, vlans] = await Promise.all([get('/config/candidate'), get('/config/running'), get('/vlans?source=candidate')])
      ib = $state.snapshot(cand.management.inband)
      if (!ib.vlan) ib.vlan = vlans.find((v) => v.id !== 1)?.id || 1
    } catch (e) { errorToast(e) }
  }
  $effect(() => { app.version; load() })

  const ports = $derived(cand?.ports || [])
  function membership(c, portName, vid) {
    const p = c?.ports?.find((x) => x.name === portName)
    if (!p) return ''
    const untagged = p.mode === 'trunk' ? p.native : p.pvid
    if (untagged === vid) return 'U'
    if (p.mode === 'trunk' && (p.tagged || []).includes(vid)) return 'T'
    return ''
  }
  const next = { '': 'U', U: 'T', T: '' }
  async function cycle(portName, vid) {
    if (!isAdmin()) return
    const cur = membership(cand, portName, vid)
    const to = next[cur]
    busyCell = portName + '/' + vid
    try {
      await patch('/vlans/matrix', { changes: [{ port: portName, vlan: vid, membership: to }] })
      await refreshStatus(); bump()
    } catch (e) { errorToast(e) }
    busyCell = ''
  }

  async function create(e) {
    e.preventDefault()
    const id = Number(newId)
    if (!Number.isInteger(id) || id < 1 || id > 4094) { toast('VLAN ID must be 1-4094', 'bad'); return }
    try {
      await post('/vlans', { id, name: newName || `VLAN${String(id).padStart(4, '0')}` })
      newId = ''; newName = ''
      toast(`VLAN ${id} staged`, 'ok'); await refreshStatus(); bump()
    } catch (err) { errorToast(err) }
  }
  async function remove(v) {
    if (!confirm(`Delete VLAN ${v.id} (${v.name})? Access ports in it move to VLAN 1.`)) return
    try {
      const r = await del('/vlans/' + v.id)
      if (r.moved_to_vlan1?.length) toast(`Moved to VLAN 1: ${r.moved_to_vlan1.join(', ')}`, 'warn')
      await refreshStatus(); bump()
    } catch (err) { errorToast(err) }
  }
  async function rename() {
    try { await patch('/vlans/' + renaming.id, { name: renaming.name }); renaming = null; await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }

  // in-band management
  let ib = $state(null)
  const ibPorts = $derived(ib ? ports.filter((p) => membership(cand, p.name, Number(ib.vlan)) !== '') : [])
  async function saveInband() {
    const body = { inband: { ...ib, vlan: Number(ib.vlan) } }
    if (!ib.enabled) body.inband = { enabled: false, vlan: 0, ipv4: { mode: 'none' }, ipv6: { mode: 'none' } }
    try { await patch('/management', body); toast('In-band management staged', 'ok'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }
</script>

<div class="page-head">
  <div><h1>VLANs</h1><p>802.1Q VLANs. U = untagged (access / native), T = tagged (trunk).</p></div>
</div>

<div class="tabs" role="tablist">
  <button class:active={tab === 'matrix'} onclick={() => (tab = 'matrix')} role="tab">Membership matrix</button>
  <button class:active={tab === 'list'} onclick={() => (tab = 'list')} role="tab">VLAN list</button>
  <button class:active={tab === 'inband'} onclick={() => (tab = 'inband')} role="tab">In-band management</button>
</div>

{#if cand}
  {#if tab === 'list'}
    <div class="grid" style="grid-template-columns:minmax(0,2fr) minmax(0,1fr)">
      <div class="card"><div class="card-body flush table-wrap">
        <table>
          <thead><tr><th>ID</th><th>Name</th><th>Untagged ports</th><th>Tagged ports</th><th></th></tr></thead>
          <tbody>
            {#each vlans as v (v.id)}
              <tr>
                <td><b>{v.id}</b></td>
                <td>{v.name} {#if v.management}<span class="badge warn">management</span>{/if}{#if !run.vlans.some((x) => x.id === v.id)}<span class="badge info">new</span>{/if}</td>
                <td class="small">{v.untagged.join(', ') || '—'}</td>
                <td class="small">{v.tagged.join(', ') || '—'}</td>
                <td class="right nowrap">{#if isAdmin()}<button class="sm" onclick={() => (renaming = { id: v.id, name: v.name })}>Rename</button>{#if v.id !== 1}<button class="sm danger" onclick={() => remove(v)}>Delete</button>{/if}{/if}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div></div>
      {#if isAdmin()}
        <form class="card" onsubmit={create}>
          <div class="card-head"><h2>Create VLAN</h2></div>
          <div class="card-body stack">
            <div class="field"><label for="vi">VLAN ID</label><input id="vi" type="number" min="1" max="4094" bind:value={newId} required data-testid="new-vlan-id" /></div>
            <div class="field"><label for="vn">Name</label><input id="vn" bind:value={newName} maxlength="32" data-testid="new-vlan-name" /></div>
            <button class="primary" type="submit" data-testid="create-vlan">Add VLAN</button>
          </div>
        </form>
      {/if}
    </div>
  {:else if tab === 'matrix'}
    <div class="card">
      <div class="card-body flush table-wrap">
        <table class="matrix" data-testid="vlan-matrix">
          <thead>
            <tr><th class="sticky">VLAN</th>{#each ports as p}<th class="pc" title={p.description}>{p.name}</th>{/each}</tr>
          </thead>
          <tbody>
            {#each vlans as v (v.id)}
              <tr>
                <th class="sticky vh">{v.id} <span class="faint">{v.name}</span>{#if v.management}<span class="badge warn">mgmt</span>{/if}</th>
                {#each ports as p}
                  {@const m = membership(cand, p.name, v.id)}
                  {@const changed = m !== membership(run, p.name, v.id)}
                  <td class="mc">
                    <button
                      class="cell {m === 'U' ? 'u' : m === 'T' ? 't' : ''}"
                      class:changed
                      disabled={!isAdmin() || busyCell === p.name + '/' + v.id}
                      onclick={() => cycle(p.name, v.id)}
                      title="{p.name} in VLAN {v.id}: {m === 'U' ? 'untagged' : m === 'T' ? 'tagged' : 'not a member'} – click to change"
                      data-cell="{p.name}/{v.id}"
                      aria-label="{p.name} VLAN {v.id} {m || 'none'}"
                    >{m || '·'}</button>
                  </td>
                {/each}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
    <div class="row small muted" style="margin-top:8px">
      <span><span class="cell u demo">U</span> untagged</span><span><span class="cell t demo">T</span> tagged</span><span><span class="cell demo">·</span> not a member</span>
      <span><span class="cell u demo changed">U</span> staged change</span>
      <span>Click cycles: not a member → untagged → tagged. Tagging a VLAN on an access port turns it into a trunk.</span>
    </div>
  {:else if tab === 'inband' && ib}
    <div class="card" style="max-width:760px">
      <div class="card-head"><h2>In-band management</h2></div>
      <div class="card-body stack">
        <div class="notice warn"><b>Careful:</b> in-band management makes the web UI reachable from switch ports in the chosen VLAN (via the interface <code>{cand.bridge.name}.{ib.vlan}</code>). Removing a port from this VLAN or changing its tagging can cut off your access. Commit-confirm rolls back if you lose access. The dedicated management port <b>{cand.management.interface}</b> keeps working.</div>
        <label class="check"><input type="checkbox" bind:checked={ib.enabled} disabled={!isAdmin()} /> Enable in-band management</label>
        {#if ib.enabled}
          <div class="form-grid">
            <div class="field"><label for="iv">Management VLAN</label><select id="iv" bind:value={ib.vlan}>{#each vlans as v}<option value={v.id}>{v.id} – {v.name}</option>{/each}</select>
              <div class="hint">Ports in this VLAN: {ibPorts.map((p) => p.name).join(', ') || 'none!'}</div></div>
            <div class="field"><label for="im">IPv4</label><select id="im" bind:value={ib.ipv4.mode}><option value="static">Static</option><option value="dhcp">DHCP</option><option value="none">None</option></select></div>
            {#if ib.ipv4.mode === 'static'}
              <div class="field"><label for="ia">Address (CIDR)</label><input id="ia" bind:value={ib.ipv4.address} placeholder="10.99.0.2/24" /></div>
              <div class="field"><label for="ig">Gateway (optional)</label><input id="ig" bind:value={ib.ipv4.gateway} /><div class="hint">Only one default gateway may be set (management or in-band).</div></div>
            {/if}
            <div class="field"><label for="i6">IPv6</label><select id="i6" bind:value={ib.ipv6.mode}><option value="none">None</option><option value="slaac">SLAAC</option><option value="static">Static</option></select></div>
            {#if ib.ipv6.mode === 'static'}<div class="field"><label for="i6a">IPv6 address</label><input id="i6a" bind:value={ib.ipv6.address} placeholder="2001:db8::2/64" /></div>{/if}
          </div>
          {#if !ibPorts.length}<div class="notice bad">No switch port is a member of VLAN {ib.vlan}; in-band management would be unreachable.</div>{/if}
        {/if}
        {#if isAdmin()}<div><button class="primary" onclick={saveInband}>Stage</button></div>{/if}
      </div>
    </div>
  {/if}
{/if}

{#if renaming}
  <Modal title="Rename VLAN {renaming.id}" onclose={() => (renaming = null)}>
    <div class="field"><label for="rn">Name</label><input id="rn" bind:value={renaming.name} maxlength="32" /></div>
    {#snippet footer()}<button onclick={() => (renaming = null)}>Cancel</button><button class="primary" onclick={rename}>Stage</button>{/snippet}
  </Modal>
{/if}

<style>
  .matrix th, .matrix td { padding: 4px 6px; text-align: center; }
  .matrix .sticky { position: sticky; left: 0; background: var(--surface-2); text-align: left; z-index: 1; white-space: nowrap; }
  .matrix .vh { font-weight: 600; }
  .matrix .pc { font-size: 11.5px; writing-mode: vertical-rl; transform: rotate(180deg); height: 72px; vertical-align: bottom; }
  .mc { border-left: 1px solid var(--border); }
  .cell { width: 32px; height: 28px; padding: 0; font-weight: 700; font-size: 12px; border-radius: 5px; border: 1px solid var(--border); background: var(--surface); color: var(--text-3); }
  .cell.u { background: var(--ok-soft); color: var(--ok); border-color: color-mix(in srgb, var(--ok) 40%, transparent); }
  .cell.t { background: var(--accent-soft); color: var(--accent); border-color: color-mix(in srgb, var(--accent) 40%, transparent); }
  .cell.changed { box-shadow: 0 0 0 2px var(--warn); }
  .cell.demo { display: inline-grid; place-items: center; width: 22px; height: 20px; font-size: 11px; vertical-align: middle; }
</style>
