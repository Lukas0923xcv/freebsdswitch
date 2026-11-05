<script>
  import { get, patch } from '../../lib/api.js'
  import { app, isAdmin, toast, errorToast, refreshStatus, bump } from '../../lib/store.svelte.js'
  let s = $state(null)
  let syslog = $state('')
  async function load() {
    s = await get('/services')
    syslog = s.syslog.servers.join(', ')
  }
  $effect(() => { app.version; load().catch(errorToast) })
  async function save(e) {
    e.preventDefault()
    try {
      await patch('/services', { snmp: s.snmp, lldp: s.lldp, syslog: { servers: syslog.split(/[\s,]+/).filter(Boolean) } })
      toast('Services staged', 'ok'); await refreshStatus(); bump()
    } catch (err) { errorToast(err) }
  }
</script>

{#if s}
<form class="grid cols-3" onsubmit={save}>
  <div class="card"><div class="card-head"><h2>SNMP</h2></div><div class="card-body stack">
    <label class="check"><input type="checkbox" bind:checked={s.snmp.enabled} disabled={!isAdmin()} /> Enable SNMP agent (bsnmpd, read-only v2c)</label>
    <div class="field"><label for="sc">Community</label><input id="sc" bind:value={s.snmp.community} disabled={!isAdmin() || !s.snmp.enabled} /></div>
    <div class="field"><label for="sl">Location</label><input id="sl" bind:value={s.snmp.location} disabled={!isAdmin() || !s.snmp.enabled} /></div>
    <div class="field"><label for="sct">Contact</label><input id="sct" bind:value={s.snmp.contact} disabled={!isAdmin() || !s.snmp.enabled} /></div>
    <div class="small faint">MIB-II (interfaces, counters) and BRIDGE-MIB via snmp_bridge. Restrict access with "allowed management subnets" or a firewall upstream.</div>
  </div></div>
  <div class="card"><div class="card-head"><h2>LLDP</h2></div><div class="card-body stack">
    <label class="check"><input type="checkbox" bind:checked={s.lldp.enabled} disabled={!isAdmin()} /> Enable LLDP (lldpd)</label>
    {#if !s.lldpd_installed}<div class="notice warn small">lldpd is not installed. Install it with <code>pkg install lldpd</code> (or <code>install.sh --with-lldp</code>; the ISO can include it).</div>{/if}
    <div class="small faint">Announces this switch and discovers neighbors on the management interface and all switch ports. See the LLDP page.</div>
  </div></div>
  <div class="card"><div class="card-head"><h2>Remote syslog</h2></div><div class="card-body stack">
    <div class="field"><label for="ss">Servers</label><input id="ss" bind:value={syslog} placeholder="192.168.1.5, logs.example:514" disabled={!isAdmin()} /><div class="hint">UDP, host[:port], comma separated.</div></div>
  </div></div>
  {#if isAdmin()}<div><button class="primary" type="submit">Stage</button></div>{/if}
</form>
{/if}
