<script>
  import { get } from '../lib/api.js'
  import { app, navigate } from '../lib/store.svelte.js'
  import { duration, bytes, bps, pps } from '../lib/format.js'
  import FrontPanel from '../components/FrontPanel.svelte'
  import Chart from '../components/Chart.svelte'

  let sys = $state(null)
  let ports = $state([])
  let history = $state([])
  let range = $state(300)

  async function load() {
    try {
      ;[sys, ports] = await Promise.all([get('/system'), get('/ports')])
    } catch {}
  }
  async function loadHistory() {
    try {
      history = await get('/stats/total?since=' + Math.floor(Date.now() / 1000 - 3600))
    } catch {}
  }
  $effect(() => { app.version; app.linkVersion; load() })
  $effect(() => { loadHistory(); const t = setInterval(load, 5000); return () => clearInterval(t) })
  // append live samples
  $effect(() => {
    const t = app.live?.total
    if (t && t.t && (!history.length || history[history.length - 1].t < t.t)) {
      history.push(t)
      if (history.length > 3600) history.splice(0, history.length - 3600)
    }
  })
  const livePorts = $derived(ports.map((p) => ({ ...p, rate: app.live?.ports?.[p.name] || p.rate })))
  const up = $derived(ports.filter((p) => p.link_up && !p.shutdown).length)
  const errors = $derived(ports.reduce((a, p) => a + (p.counters.rx_errors || 0) + (p.counters.tx_errors || 0), 0))
  const top = $derived([...livePorts].sort((a, b) => (b.rate.rx_bps + b.rate.tx_bps) - (a.rate.rx_bps + a.rate.tx_bps)).slice(0, 5))
  const mem = $derived(sys?.mem_total ? Math.round((100 * sys.mem_used) / sys.mem_total) : 0)

  function select(name) {
    if (name === '__mgmt') navigate('/system?tab=management')
    else navigate('/ports?edit=' + encodeURIComponent(name))
  }
</script>

<div class="stack" style="gap:16px">
  <div class="card">
    <div class="card-body" style="padding:12px">
      <FrontPanel ports={livePorts} mgmt={sys?.management} hostname={sys?.hostname} onselect={select} />
    </div>
  </div>

  <div class="grid cols-4">
    <div class="card stat"><div class="card-body"><div class="label">Ports up</div><div class="value">{up}<span class="faint"> / {ports.length}</span></div></div></div>
    <div class="card stat"><div class="card-body"><div class="label">Total throughput</div><div class="value">{bps((app.live?.total?.rx_bps || 0) + (app.live?.total?.tx_bps || 0))}</div><div class="small faint">{pps((app.live?.total?.rx_pps || 0) + (app.live?.total?.tx_pps || 0))}</div></div></div>
    <div class="card stat"><div class="card-body"><div class="label">VLANs</div><div class="value">{sys?.vlan_count ?? '—'}</div><div class="small faint">{sys?.stp_root ? 'STP root bridge' : 'STP non-root'}</div></div></div>
    <div class="card stat"><div class="card-body"><div class="label">Port errors</div><div class="value" class:bad={errors > 0}>{errors}</div><div class="small faint">since last counter reset</div></div></div>
  </div>

  <div class="grid cols-3" style="grid-template-columns: minmax(0,2fr) minmax(0,1fr)">
    <div class="card">
      <div class="card-head">
        <h2>Throughput (all switch ports)</h2>
        <select style="width:auto" bind:value={range} aria-label="time range">
          <option value={300}>5 min</option><option value={900}>15 min</option><option value={3600}>1 hour</option>
        </select>
      </div>
      <div class="card-body"><Chart samples={history} window={range} height={220} /></div>
    </div>
    <div class="card">
      <div class="card-head"><h2>System</h2></div>
      <div class="card-body">
        {#if sys}
          <dl class="kv">
            <dt>Hostname</dt><dd>{sys.hostname}</dd>
            <dt>Uptime</dt><dd>{duration(sys.uptime_sec)}</dd>
            <dt>FreeBSD</dt><dd>{sys.os_release} ({sys.machine})</dd>
            <dt>bsdswitch</dt><dd>{sys.version}</dd>
            <dt>CPU</dt><dd><div class="meter"><i style="width:{Math.min(100, sys.cpu_percent)}%"></i></div><span class="small">{sys.cpu_percent.toFixed(0)}% · {sys.ncpu} cores</span></dd>
            <dt>Memory</dt><dd><div class="meter"><i style="width:{mem}%"></i></div><span class="small">{bytes(sys.mem_used)} / {bytes(sys.mem_total)}</span></dd>
            <dt>Management</dt><dd>{sys.management.interface} <span class="dot {sys.management.link_up ? 'ok' : 'bad'}"></span><br /><span class="small mono">{sys.management.addresses.join(' ') || 'no address'}</span></dd>
            {#if sys.management.inband}<dt>In-band</dt><dd>{sys.management.inband} <span class="small mono">{sys.management.inband_addresses.join(' ')}</span></dd>{/if}
            <dt>Web UI</dt><dd class="small">{#each sys.management.urls as u}<div class="mono">{u}</div>{/each}</dd>
            <dt>Config</dt><dd>
              {#if sys.config.pending}<span class="badge warn">awaiting confirm</span>
              {:else if sys.config.staged}<span class="badge info">changes staged</span>
              {:else if sys.config.startup_differs}<span class="badge warn">not saved</span>
              {:else}<span class="badge ok">saved</span>{/if}
              {#if sys.config.boot_mode === 'safe'}<span class="badge bad">safe mode</span>{/if}
            </dd>
          </dl>
        {/if}
      </div>
    </div>
  </div>

  <div class="card">
    <div class="card-head"><h2>Busiest ports</h2><a href="/ports" onclick={(e) => { e.preventDefault(); navigate('/ports') }}>All ports →</a></div>
    <div class="card-body flush table-wrap">
      <table>
        <thead><tr><th>Port</th><th>Description</th><th>Link</th><th class="num">RX</th><th class="num">TX</th><th class="num">RX pps</th><th class="num">TX pps</th></tr></thead>
        <tbody>
          {#each top as p (p.name)}
            <tr>
              <td><b>{p.name}</b></td><td class="muted">{p.description}</td>
              <td><span class="dot {p.link_up && !p.shutdown ? 'ok' : ''}"></span></td>
              <td class="num">{bps(p.rate.rx_bps)}</td><td class="num">{bps(p.rate.tx_bps)}</td>
              <td class="num">{pps(p.rate.rx_pps)}</td><td class="num">{pps(p.rate.tx_pps)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>
</div>

<style>
  .stat .label { font-size: 12px; color: var(--text-2); }
  .stat .value { font-size: 24px; font-weight: 650; font-variant-numeric: tabular-nums; margin-top: 2px; }
  .stat .value.bad { color: var(--bad); }
  .meter { height: 6px; border-radius: 3px; background: var(--surface-3); overflow: hidden; margin: 6px 0 2px; }
  .meter i { display: block; height: 100%; background: var(--accent); }
</style>
