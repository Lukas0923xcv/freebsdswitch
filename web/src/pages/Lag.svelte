<script>
  import { get, post, put, del } from '../lib/api.js'
  import { app, isAdmin, toast, errorToast, refreshStatus, bump } from '../lib/store.svelte.js'
  import Modal from '../components/Modal.svelte'

  let lags = $state([])
  let ifaces = $state([])
  let editing = $state(null)

  async function load() {
    try { ;[lags, ifaces] = await Promise.all([get('/lags'), get('/interfaces')]) } catch (e) { errorToast(e) }
  }
  $effect(() => { app.version; app.linkVersion; load() })
  $effect(() => { const t = setInterval(async () => { try { lags = await get('/lags') } catch {} }, 5000); return () => clearInterval(t) })

  const candidates = $derived(ifaces.filter((i) => i.exists && i.role !== 'management' && i.role !== 'mirror'))
  function startNew() { editing = { isNew: true, name: '', protocol: 'lacp', members: [], hash: 'l2,l3,l4', lacp_fast: false } }
  function startEdit(l) { editing = { isNew: false, name: l.name, protocol: l.protocol, members: [...l.members], hash: l.hash || '', lacp_fast: !!l.lacp_fast } }
  function toggle(m) { editing.members = editing.members.includes(m) ? editing.members.filter((x) => x !== m) : [...editing.members, m] }
  async function save() {
    const body = { name: editing.name, protocol: editing.protocol, members: editing.members, hash: editing.protocol === 'lacp' || editing.protocol === 'loadbalance' ? editing.hash : '', lacp_fast: editing.lacp_fast }
    try {
      if (editing.isNew) await post('/lags', body)
      else await put('/lags/' + editing.name, body)
      editing = null
      toast('LAG staged. Configure its VLANs on the Ports page.', 'ok')
      await refreshStatus(); bump()
    } catch (e) { errorToast(e) }
  }
  async function remove(l) {
    if (!confirm(`Delete ${l.name}? Its members become individual ports with the LAG's VLAN settings.`)) return
    try { await del('/lags/' + l.name); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }
  function memberBadge(p) {
    if (!p.link) return ['no link', '']
    return p.active ? ['active', 'ok'] : ['standby', 'warn']
  }
</script>

<div class="page-head">
  <div><h1>Link aggregation</h1><p>Bundle ports with lagg(4). The LAG becomes one switch port; its VLANs and STP settings are edited on the Ports page.</p></div>
  {#if isAdmin()}<button class="primary" onclick={startNew}>New LAG</button>{/if}
</div>

<div class="stack">
  {#each lags as l (l.name)}
    <div class="card">
      <div class="card-head">
        <div class="row"><h2>{l.name}</h2><span class="badge info">{l.protocol}</span>{#if l.exists}<span class="badge {l.up ? 'ok' : ''}">{l.up ? 'up' : 'down'}</span>{:else}<span class="badge warn">staged</span>{/if}</div>
        {#if isAdmin()}<div class="row"><button class="sm" onclick={() => startEdit(l)}>Edit</button><button class="sm danger" onclick={() => remove(l)}>Delete</button></div>{/if}
      </div>
      <div class="card-body">
        <dl class="kv">
          <dt>Members</dt><dd>{l.members.join(', ')}</dd>
          {#if l.hash}<dt>Hash</dt><dd>{l.hash}</dd>{/if}
          {#if l.port}<dt>VLANs</dt><dd>{l.port.mode === 'trunk' ? `trunk, native ${l.port.native || 'none'}, tagged ${(l.port.tagged || []).join(',') || '–'}` : `access VLAN ${l.port.pvid}`}</dd>{/if}
          {#if l.lacp_id}<dt>LACP partner</dt><dd class="mono small">{l.lacp_id}</dd>{/if}
        </dl>
        {#if l.ports.length}
          <table style="margin-top:12px">
            <thead><tr><th>Member</th><th>Status</th><th>Flags</th></tr></thead>
            <tbody>{#each l.ports as p}{@const [b, c] = memberBadge(p)}<tr><td>{p.name}</td><td><span class="badge {c}">{b}</span></td><td class="small mono">{p.flags.join(' ')}</td></tr>{/each}</tbody>
          </table>
        {/if}
      </div>
    </div>
  {:else}
    <div class="card"><div class="empty">No link aggregation groups.</div></div>
  {/each}
</div>
<p class="small faint">LACP needs a partner that speaks 802.3ad. Proxmox Linux bridges cannot forward LACP frames, so test LACP on real hardware or with the epair lab. All members must use the same speed.</p>

{#if editing}
  <Modal title={editing.isNew ? 'New LAG' : 'Edit ' + editing.name} onclose={() => (editing = null)}>
    <div class="stack">
      <div class="field"><label for="lp">Protocol</label>
        <select id="lp" bind:value={editing.protocol}>
          <option value="lacp">LACP (802.3ad)</option><option value="failover">Failover (active/backup)</option>
          <option value="loadbalance">Static load balance</option><option value="roundrobin">Round robin</option>
        </select></div>
      {#if editing.protocol === 'lacp' || editing.protocol === 'loadbalance'}
        <div class="field"><label for="lh">Hash layers</label><select id="lh" bind:value={editing.hash}><option value="l2">l2</option><option value="l2,l3">l2,l3</option><option value="l2,l3,l4">l2,l3,l4</option><option value="l3,l4">l3,l4</option></select></div>
      {/if}
      {#if editing.protocol === 'lacp'}<label class="check"><input type="checkbox" bind:checked={editing.lacp_fast} /> LACP fast timeout (1 s)</label>{/if}
      <div class="field"><label>Members</label>
        {#each candidates as i}
          {@const inOther = i.role.startsWith('lag-member') && !i.role.includes(editing.name || '#')}
          <label class="check"><input type="checkbox" checked={editing.members.includes(i.name)} disabled={inOther} onchange={() => toggle(i.name)} /> {i.name} <span class="faint small">{i.driver} · {i.link_up ? 'link up' : 'no link'} · {i.role}</span></label>
        {/each}</div>
      <div class="notice small">Members are removed from the switch; a new LAG takes over the VLAN settings of its first member.</div>
    </div>
    {#snippet footer()}<button onclick={() => (editing = null)}>Cancel</button><button class="primary" onclick={save} disabled={!editing.members.length}>Stage</button>{/snippet}
  </Modal>
{/if}
