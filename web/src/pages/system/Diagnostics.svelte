<script>
  import { get, post } from '../../lib/api.js'
  import { isAdmin, errorToast } from '../../lib/store.svelte.js'
  import { bytes } from '../../lib/format.js'
  let host = $state('')
  let count = $state(4)
  let out = $state('')
  let busy = $state(false)
  let ports = $state([])
  let cap = $state({ port: '', count: 1000, seconds: 30, filter: '' })
  let job = $state(null)
  get('/config/running').then((c) => { ports = [c.management.interface, ...c.ports.map((p) => p.name), c.bridge.name]; cap.port = c.ports[0]?.name || c.management.interface })
  async function run(kind) {
    busy = true; out = 'running…'
    try { const r = await post('/diag/' + kind, { host, count: Number(count) }); out = r.output + (r.error ? '\n' + r.error : '') } catch (e) { out = e.detail }
    busy = false
  }
  async function capture() {
    try {
      job = await post('/diag/capture', { ...cap, count: Number(cap.count), seconds: Number(cap.seconds) })
      const poll = setInterval(async () => {
        try { job = await get('/diag/capture/' + job.id); if (job.done) clearInterval(poll) } catch { clearInterval(poll) }
      }, 1000)
    } catch (e) { errorToast(e) }
  }
</script>

{#if !isAdmin()}<div class="notice">Diagnostics require the admin role.</div>{:else}
<div class="grid cols-2">
  <div class="card">
    <div class="card-head"><h2>Ping / traceroute</h2></div>
    <div class="card-body stack">
      <div class="row">
        <input class="grow" bind:value={host} placeholder="host or IP" style="width:auto" aria-label="host" />
        <input type="number" min="1" max="20" bind:value={count} style="width:72px" aria-label="count" />
        <button onclick={() => run('ping')} disabled={busy || !host}>Ping</button>
        <button onclick={() => run('traceroute')} disabled={busy || !host}>Traceroute</button>
      </div>
      <pre style="min-height:200px;max-height:50vh">{out}</pre>
      <div class="small faint">Sent from the switch's own management addresses.</div>
    </div>
  </div>
  <div class="card">
    <div class="card-head"><h2>Packet capture</h2></div>
    <div class="card-body stack">
      <div class="form-grid">
        <div class="field"><label for="cp">Interface</label><select id="cp" bind:value={cap.port}>{#each ports as p}<option value={p}>{p}</option>{/each}</select></div>
        <div class="field"><label for="cf">Filter (pcap syntax)</label><input id="cf" bind:value={cap.filter} placeholder="e.g. vlan 10 or port 53" /></div>
        <div class="field"><label for="cc">Max packets</label><input id="cc" type="number" min="1" max="100000" bind:value={cap.count} /></div>
        <div class="field"><label for="cs">Max seconds</label><input id="cs" type="number" min="1" max="60" bind:value={cap.seconds} /></div>
      </div>
      <div><button class="primary" onclick={capture} disabled={job && !job.done}>Start capture</button></div>
      {#if job}
        <div class="notice">
          {#if !job.done}Capturing on {job.port}…{:else if job.error}Failed: {job.error}{:else}Done ({bytes(job.size)}). <a href="/api/v1/diag/capture/{job.id}.pcap" download>Download pcap</a>{/if}
        </div>
      {/if}
      <div class="small faint">Captures on a bridge member see frames as received on the wire, including 802.1Q tags. Open the file in Wireshark.</div>
    </div>
  </div>
</div>
{/if}
