<script>
  import { get } from '../lib/api.js'
  import { app, errorToast, navigate } from '../lib/store.svelte.js'
  let n = $state([])
  let enabled = $state(true)
  async function load() {
    try {
      const s = await get('/config/running')
      enabled = s.lldp?.enabled
      n = enabled ? await get('/lldp/neighbors') : []
    } catch (e) { errorToast(e) }
  }
  $effect(() => { app.version; load(); const t = setInterval(load, 15000); return () => clearInterval(t) })
</script>

<div class="page-head"><div><h1>LLDP neighbors</h1><p>Devices announcing themselves via LLDP on each port (lldpd).</p></div></div>
{#if !enabled}
  <div class="notice">LLDP is disabled. Enable it under <a href="/system?tab=services" onclick={(e) => { e.preventDefault(); navigate('/system?tab=services') }}>System › Services</a> (requires the lldpd package).</div>
{:else}
  <div class="card"><div class="card-body flush table-wrap">
    <table>
      <thead><tr><th>Local port</th><th>System</th><th>Remote port</th><th>Chassis ID</th><th>Management IP</th><th>Capabilities</th><th>Seen for</th></tr></thead>
      <tbody>
        {#each n as x}
          <tr><td><b>{x.local_port}</b></td><td>{x.system_name}<div class="small faint">{x.system_descr}</div></td><td>{x.port_id}<div class="small faint">{x.port_descr}</div></td>
            <td class="mono small">{x.chassis_id}</td><td class="mono small">{x.mgmt_ip.join(' ')}</td><td>{#each x.capabilities as c}<span class="badge">{c}</span> {/each}</td><td class="small">{x.age}</td></tr>
        {:else}<tr><td colspan="7" class="empty">No neighbors discovered yet.</td></tr>{/each}
      </tbody>
    </table>
  </div></div>
{/if}
