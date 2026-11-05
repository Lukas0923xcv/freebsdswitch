<script>
  import { get, patch } from '../lib/api.js'
  import { app, isAdmin, toast, errorToast, refreshStatus, bump } from '../lib/store.svelte.js'

  let data = $state(null)
  let cfg = $state(null)
  let cand = $state(null)

  async function load() {
    try {
      ;[data, cand] = await Promise.all([get('/stp'), get('/config/candidate')])
      cfg = $state.snapshot(cand.bridge.stp)
    } catch (e) { errorToast(e) }
  }
  $effect(() => { app.version; load() })
  $effect(() => { const t = setInterval(async () => { try { data = await get('/stp') } catch {} }, 4000); return () => clearInterval(t) })

  const priorities = Array.from({ length: 16 }, (_, i) => i * 4096)
  async function save() {
    try { await patch('/bridge', { stp: { ...cfg, priority: Number(cfg.priority), hello: Number(cfg.hello), max_age: Number(cfg.max_age), fwd_delay: Number(cfg.fwd_delay) } }); toast('Spanning tree settings staged', 'ok'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }
  async function setEdge(name, edge) {
    try { await patch('/ports/' + name, { stp: { edge } }); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }
  function stateBadge(s) {
    if (s === 'forwarding') return 'ok'
    if (s === 'discarding' || s === 'blocking') return 'warn'
    return ''
  }
  const candPort = (n) => cand?.ports?.find((p) => p.name === n)
</script>

<div class="page-head"><div><h1>Spanning tree</h1><p>RSTP (802.1w) with STP fallback. One spanning tree instance covers all VLANs (MSTP/PVST are not supported by if_bridge).</p></div></div>

{#if data && cfg}
  <div class="grid cols-2">
    <div class="card">
      <div class="card-head"><h2>Bridge settings</h2></div>
      <div class="card-body form-grid">
        <div class="field full"><label class="check"><input type="checkbox" bind:checked={cfg.enabled} disabled={!isAdmin()} /> Spanning tree enabled</label></div>
        <div class="field"><label for="sm">Protocol</label><select id="sm" bind:value={cfg.mode} disabled={!isAdmin()}><option value="rstp">RSTP</option><option value="stp">STP (legacy)</option></select></div>
        <div class="field"><label for="sp">Bridge priority</label><select id="sp" bind:value={cfg.priority} disabled={!isAdmin()}>{#each priorities as p}<option value={p}>{p}{p === 32768 ? ' (default)' : ''}{p === 0 ? ' (root)' : ''}</option>{/each}</select></div>
        <div class="field"><label for="sh">Hello time (s)</label><input id="sh" type="number" min="1" max="2" bind:value={cfg.hello} disabled={!isAdmin()} /></div>
        <div class="field"><label for="sa">Max age (s)</label><input id="sa" type="number" min="6" max="40" bind:value={cfg.max_age} disabled={!isAdmin()} /></div>
        <div class="field"><label for="sf">Forward delay (s)</label><input id="sf" type="number" min="4" max="30" bind:value={cfg.fwd_delay} disabled={!isAdmin()} /></div>
        {#if isAdmin()}<div class="field full"><button class="primary" onclick={save}>Stage</button></div>{/if}
      </div>
    </div>
    <div class="card">
      <div class="card-head"><h2>Root bridge</h2>{#if data.bridge?.is_root}<span class="badge ok">this switch is root</span>{/if}</div>
      <div class="card-body">
        {#if data.bridge}
          <dl class="kv">
            <dt>Root ID</dt><dd class="mono">{data.bridge.root_priority} / {data.bridge.root_id}</dd>
            <dt>Root path cost</dt><dd>{data.bridge.root_cost}</dd>
            <dt>Root port</dt><dd>{data.ports.find((p) => p.is_root_port)?.name || (data.bridge.is_root ? '— (this bridge)' : data.bridge.root_port)}</dd>
            <dt>This bridge</dt><dd class="mono">{data.bridge.priority} / {data.bridge.id}</dd>
            <dt>Protocol</dt><dd>{data.bridge.proto}</dd>
            <dt>Timers</dt><dd>hello {data.bridge.hello}s · max age {data.bridge.max_age}s · forward delay {data.bridge.fwd_delay}s</dd>
          </dl>
        {:else}<div class="faint">Bridge not configured.</div>{/if}
      </div>
    </div>
  </div>
  <div class="card" style="margin-top:16px">
    <div class="card-head"><h2>Ports</h2></div>
    <div class="card-body flush table-wrap">
      <table>
        <thead><tr><th>Port</th><th>Role</th><th>State</th><th class="num">Path cost</th><th class="num">Priority</th><th>Edge (admin)</th><th>Edge (oper)</th><th>Point-to-point</th></tr></thead>
        <tbody>
          {#each data.ports as p (p.name)}
            <tr>
              <td><b>{p.name}</b>{#if p.is_root_port}<span class="badge info">root port</span>{/if}</td>
              {#if p.stp}
                <td>{p.role}</td><td><span class="badge {stateBadge(p.state)}">{p.state}</span></td>
                <td class="num">{p.path_cost}{p.admin_cost ? '' : ' (auto)'}</td><td class="num">{p.priority}</td>
                <td><input type="checkbox" checked={candPort(p.name)?.stp?.edge} disabled={!isAdmin()} onchange={(e) => setEdge(p.name, e.currentTarget.checked)} aria-label="edge {p.name}" /></td>
                <td>{p.oper_edge ? 'yes' : 'no'}</td><td>{p.ptp ? 'yes' : 'no'}</td>
              {:else}
                <td colspan="7" class="faint">STP disabled on this port</td>
              {/if}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>
  <p class="small faint">Per-port cost and priority are edited in the port dialog (Ports page).</p>
{/if}
