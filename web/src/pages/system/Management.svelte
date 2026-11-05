<script>
  import { get, patch } from '../../lib/api.js'
  import { app, isAdmin, toast, errorToast, refreshStatus, bump } from '../../lib/store.svelte.js'
  let m = $state(null)
  let ifaces = $state([])
  let subnets = $state('')
  async function load() {
    const [c, i] = await Promise.all([get('/config/candidate'), get('/interfaces')])
    m = $state.snapshot(c.management)
    subnets = (m.allowed_subnets || []).join(', ')
    ifaces = i.filter((x) => x.exists)
  }
  $effect(() => { app.version; load().catch(errorToast) })
  async function save(e) {
    e.preventDefault()
    const body = { ...m, allowed_subnets: subnets.split(/[\s,]+/).filter(Boolean),
      ssh: { ...m.ssh, port: Number(m.ssh.port) }, https_port: Number(m.https_port), http_port: Number(m.http_port), session_timeout: Number(m.session_timeout) }
    if (body.ipv4.mode !== 'static') body.ipv4 = { mode: body.ipv4.mode }
    if (body.ipv6.mode !== 'static') body.ipv6 = { mode: body.ipv6.mode }
    try { await patch('/management', body); toast('Management settings staged – review the impact before applying', 'warn'); await refreshStatus(); bump() } catch (err) { errorToast(err) }
  }
</script>

{#if m}
  <form class="stack" style="max-width:860px" onsubmit={save}>
    <div class="notice warn">Changes here can disconnect you. They are applied with commit-confirm: if you cannot reach the switch at its new address within the timeout, it rolls back by itself. The console menu (options 1 and 2) can always fix the management settings.</div>
    <div class="card">
      <div class="card-head"><h2>Management interface</h2></div>
      <div class="card-body form-grid">
        <div class="field"><label for="mi">Interface</label>
          <select id="mi" bind:value={m.interface} disabled={!isAdmin()}>{#each ifaces as i}<option value={i.name}>{i.name} ({i.driver}, {i.link_up ? 'link up' : 'no link'}){i.role === 'port' ? ' – currently a switch port' : ''}</option>{/each}</select>
          <div class="hint">A dedicated port outside the bridge. Selecting a switch port removes it from the switch.</div></div>
        <div></div>
        <div class="field"><label for="v4">IPv4</label><select id="v4" bind:value={m.ipv4.mode} disabled={!isAdmin()}><option value="dhcp">DHCP</option><option value="static">Static</option><option value="none">None</option></select></div>
        {#if m.ipv4.mode === 'static'}
          <div class="field"><label for="a4">Address (CIDR)</label><input id="a4" bind:value={m.ipv4.address} placeholder="192.168.1.10/24" disabled={!isAdmin()} /></div>
          <div class="field"><label for="g4">Default gateway</label><input id="g4" bind:value={m.ipv4.gateway} placeholder="192.168.1.1" disabled={!isAdmin()} /></div>
        {/if}
        <div class="field"><label for="v6">IPv6</label><select id="v6" bind:value={m.ipv6.mode} disabled={!isAdmin()}><option value="none">None</option><option value="slaac">SLAAC</option><option value="static">Static</option></select></div>
        {#if m.ipv6.mode === 'static'}
          <div class="field"><label for="a6">Address</label><input id="a6" bind:value={m.ipv6.address} placeholder="2001:db8::10/64" disabled={!isAdmin()} /></div>
          <div class="field"><label for="g6">Gateway</label><input id="g6" bind:value={m.ipv6.gateway} disabled={!isAdmin()} /></div>
        {/if}
      </div>
    </div>
    <div class="card">
      <div class="card-head"><h2>Access</h2></div>
      <div class="card-body form-grid">
        <div class="field"><label class="check"><input type="checkbox" bind:checked={m.ssh.enabled} disabled={!isAdmin()} /> SSH enabled</label>
          <label class="check" style="margin-top:6px"><input type="checkbox" bind:checked={m.ssh.password_auth} disabled={!isAdmin() || !m.ssh.enabled} /> Allow password login (otherwise keys only)</label></div>
        <div class="field"><label for="sp">SSH port</label><input id="sp" type="number" bind:value={m.ssh.port} disabled={!isAdmin()} /></div>
        <div class="field full"><label for="as">Allowed management subnets</label><input id="as" bind:value={subnets} placeholder="empty = any; e.g. 192.168.1.0/24, 2001:db8::/64" disabled={!isAdmin()} />
          <div class="hint">Web UI / API requests from other source addresses are refused.</div></div>
        <div class="field"><label for="hp">HTTPS port</label><input id="hp" type="number" bind:value={m.https_port} disabled={!isAdmin()} /><div class="hint">Takes effect after a daemon restart.</div></div>
        <div class="field"><label for="st">Session timeout (minutes)</label><input id="st" type="number" min="1" max="1440" bind:value={m.session_timeout} disabled={!isAdmin()} /></div>
        <div class="field full small muted">The web UI only answers on the management interface and the optional in-band VLAN interface (VLANs › In-band management), never on switch ports.</div>
        {#if isAdmin()}<div class="field full"><button class="primary" type="submit">Stage</button></div>{/if}
      </div>
    </div>
  </form>
{/if}
