<script>
  import { get, put } from '../lib/api.js'
  import { app, isAdmin, toast, errorToast, refreshStatus, bump } from '../lib/store.svelte.js'
  import { speed } from '../lib/format.js'

  let ifaces = $state([])
  let roles = $state({})
  let original = $state({})
  let expanded = $state(null)

  async function load() {
    try {
      ifaces = await get('/interfaces')
      const cand = await get('/config/candidate')
      const r = {}
      for (const i of ifaces) r[i.name] = 'unused'
      for (const p of cand.ports || []) if (!(cand.lags || []).some((l) => l.name === p.name)) r[p.name] = 'port'
      for (const l of cand.lags || []) for (const m of l.members) r[m] = 'lag'
      r[cand.management.interface] = 'management'
      roles = r
      original = { ...r }
    } catch (e) { errorToast(e) }
  }
  $effect(() => { app.version; load() })

  const changed = $derived(Object.keys(roles).some((k) => roles[k] !== original[k]))
  const mgmtCount = $derived(Object.values(roles).filter((r) => r === 'management').length)
  const mgmtChanged = $derived(Object.keys(roles).find((k) => roles[k] === 'management') !== Object.keys(original).find((k) => original[k] === 'management'))

  async function save() {
    if (mgmtCount !== 1) { toast('Select exactly one management interface', 'bad'); return }
    const management = Object.keys(roles).find((k) => roles[k] === 'management')
    try {
      await put('/interfaces/assignment', {
        management,
        ports: Object.keys(roles).filter((k) => roles[k] === 'port'),
        unused: Object.keys(roles).filter((k) => roles[k] === 'unused'),
      })
      toast('Assignment staged. Review and apply the pending changes.', 'ok')
      await refreshStatus(); bump()
    } catch (e) { errorToast(e) }
  }
  const capsShown = ['LRO', 'TSO4', 'TSO6', 'VLAN_HWTAGGING', 'VLAN_HWFILTER', 'VLAN_MTU', 'JUMBO_MTU', 'RXCSUM', 'TXCSUM', 'LINKSTATE', 'WOL_MAGIC']
</script>

<div class="page-head">
  <div><h1>Interface assignments</h1><p>All detected Ethernet NICs. Choose the management interface, which NICs are switch ports and which stay unused.</p></div>
  {#if isAdmin()}<button class="primary" disabled={!changed} onclick={save} data-testid="save-assignment">Stage assignment</button>{/if}
</div>
{#if mgmtChanged}
  <div class="notice warn" style="margin-bottom:12px">You are moving the management interface. After applying you must reconnect to the new management address within the confirm timeout, otherwise the change rolls back.</div>
{/if}
<div class="card">
  <div class="card-body flush table-wrap">
    <table>
      <thead><tr><th>Interface</th><th>Driver</th><th>MAC</th><th>Link</th><th>Speed</th><th>MTU</th><th>Capabilities</th><th>Role</th></tr></thead>
      <tbody>
        {#each ifaces as i (i.name)}
          <tr>
            <td><b>{i.name}</b>{#if !i.exists}<span class="badge bad">missing</span>{/if}</td>
            <td>{i.driver || '—'}</td>
            <td class="mono">{i.mac || '—'}</td>
            <td>{#if i.exists}<span class="badge {i.link_up ? 'ok' : ''}">{i.link_up ? 'up' : 'down'}</span>{/if}</td>
            <td>{speed(i.speed)}{#if i.exists && !i.speed_settable}<span class="faint small"> (fixed)</span>{/if}</td>
            <td>{i.mtu || '—'}</td>
            <td>
              <button class="ghost sm" onclick={() => (expanded = expanded === i.name ? null : i.name)}>{i.capabilities.length} caps</button>
              {#each i.notes as n}<div class="small faint">{n}</div>{/each}
            </td>
            <td>
              {#if roles[i.name] === 'lag'}
                <span class="badge info">LAG member</span>
              {:else}
                <select bind:value={roles[i.name]} disabled={!isAdmin() || !i.exists} aria-label="role of {i.name}" style="width:auto">
                  <option value="management">Management</option>
                  <option value="port">Switch port</option>
                  <option value="unused">Unused</option>
                </select>
              {/if}
            </td>
          </tr>
          {#if expanded === i.name}
            <tr><td colspan="8" style="background:var(--surface-2)">
              <div class="row" style="gap:6px">
                {#each capsShown.filter((c) => i.capabilities.includes(c) || i.options.includes(c)) as c}
                  <span class="badge {i.options.includes(c) ? 'info' : ''}" title={i.options.includes(c) ? 'enabled' : 'supported, disabled'}>{c}</span>
                {/each}
              </div>
              {#if i.supported_media?.length}<div class="small muted" style="margin-top:6px">Media: {i.supported_media.join(' · ')}</div>{/if}
              {#if i.ipv4.length || i.ipv6.length}<div class="small mono" style="margin-top:6px">{[...i.ipv4, ...i.ipv6].join('  ')}</div>{/if}
            </td></tr>
          {/if}
        {/each}
      </tbody>
    </table>
  </div>
</div>
<p class="small faint">Blue capability badges are enabled. On switch ports bsdswitch disables LRO, TSO and hardware VLAN filtering, which break or bypass bridging.</p>
