<script>
  import { get, patch } from '../../lib/api.js'
  import { app, isAdmin, toast, errorToast, refreshStatus, bump } from '../../lib/store.svelte.js'
  let f = $state(null)
  const zones = ['UTC', 'Europe/Berlin', 'Europe/Vienna', 'Europe/Zurich', 'Europe/London', 'Europe/Paris', 'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles', 'Asia/Tokyo', 'Asia/Singapore', 'Australia/Sydney']
  async function load() {
    const c = await get('/config/candidate')
    f = { ...c.system, dns: (c.system.dns || []).join(', '), ntp: (c.system.ntp || []).join(', ') }
  }
  $effect(() => { app.version; load().catch(errorToast) })
  const split = (s) => s.split(/[\s,]+/).filter(Boolean)
  async function save(e) {
    e.preventDefault()
    try {
      await patch('/system/settings', { hostname: f.hostname, domain: f.domain, timezone: f.timezone, dns: split(f.dns), ntp: split(f.ntp) })
      toast('General settings staged', 'ok'); await refreshStatus(); bump()
    } catch (err) { errorToast(err) }
  }
</script>

{#if f}
  <form class="card" style="max-width:760px" onsubmit={save}>
    <div class="card-body form-grid">
      <div class="field"><label for="h">Hostname</label><input id="h" bind:value={f.hostname} required pattern="[A-Za-z0-9]([A-Za-z0-9\-]{'{'}0,61{'}'}[A-Za-z0-9])?" disabled={!isAdmin()} /></div>
      <div class="field"><label for="d">Domain</label><input id="d" bind:value={f.domain} placeholder="lab.example" disabled={!isAdmin()} /></div>
      <div class="field"><label for="tz">Time zone</label><input id="tz" list="zones" bind:value={f.timezone} disabled={!isAdmin()} /><datalist id="zones">{#each zones as z}<option value={z}></option>{/each}</datalist></div>
      <div class="field"><label for="dns">DNS servers</label><input id="dns" bind:value={f.dns} placeholder="192.168.1.1, 1.1.1.1" disabled={!isAdmin()} /><div class="hint">Up to 3, comma separated. Leave empty when DHCP provides DNS.</div></div>
      <div class="field full"><label for="ntp">NTP servers</label><input id="ntp" bind:value={f.ntp} placeholder="pool.ntp.org" disabled={!isAdmin()} /><div class="hint">Empty disables ntpd.</div></div>
      {#if isAdmin()}<div class="field full"><button class="primary" type="submit">Stage</button></div>{/if}
    </div>
  </form>
{/if}
