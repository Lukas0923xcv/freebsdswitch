<script>
  import { get, put } from '../lib/api.js'
  import { app, isAdmin, toast, errorToast, refreshStatus, bump } from '../lib/store.svelte.js'

  let tab = $state(app.query.get('tab') || 'acl')
  let acls = $state([])
  let rates = $state([])
  let mirrors = $state([])
  let ports = $state([])
  let ifaces = $state([])
  let preview = $state(null)
  let dirty = $state(false)

  async function load() {
    try {
      const cand = await get('/config/candidate')
      ports = cand.ports.map((p) => p.name)
      ifaces = (await get('/interfaces')).filter((i) => i.exists && (i.role === 'unused' || i.role === 'mirror')).map((i) => i.name)
      acls = $state.snapshot(cand.acls || [])
      const rl = cand.rate_limits || []
      rates = ports.map((p) => { const r = rl.find((x) => x.port === p); return { port: p, ingress_kbps: r?.ingress_kbps || 0, egress_kbps: r?.egress_kbps || 0 } })
      mirrors = (cand.mirrors || []).map((m) => m.destination)
      preview = await get('/filters/preview')
      dirty = false
    } catch (e) { errorToast(e) }
  }
  $effect(() => { app.version; load() })

  function addACL() { acls.push({ name: 'acl' + (acls.length + 1), port: ports[0] || '', direction: 'in', default: 'pass', rules: [] }); dirty = true }
  function addRule(a) { a.rules.push({ action: 'block', proto: 'any', src: '', dst: '', dst_port: '', comment: '' }); dirty = true }
  function move(a, i, d) { const r = a.rules.splice(i, 1)[0]; a.rules.splice(i + d, 0, r); dirty = true }
  async function stageACLs() {
    const body = acls.map((a) => ({ ...a, rules: a.rules.map((r) => ({ ...r, proto: r.proto === 'any' ? '' : r.proto })) }))
    try { await put('/acls', body); toast('ACLs staged', 'ok'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }
  async function stageRates() {
    try { await put('/rate-limits', rates.map((r) => ({ port: r.port, ingress_kbps: Number(r.ingress_kbps) || 0, egress_kbps: Number(r.egress_kbps) || 0 }))); toast('Rate limits staged', 'ok'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }
  async function stageMirrors() {
    try { await put('/mirrors', mirrors.map((d) => ({ destination: d }))); toast('Mirroring staged', 'ok'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }
  const mirrorCandidates = $derived([...new Set([...ifaces, ...mirrors])])
</script>

<div class="page-head"><div><h1>Filtering, rate limits &amp; mirroring</h1><p>ACLs are compiled to pf rules, rate limits to dummynet pipes; mirroring uses bridge span ports.</p></div></div>
<div class="tabs" role="tablist">
  <button class:active={tab === 'acl'} onclick={() => (tab = 'acl')}>ACLs</button>
  <button class:active={tab === 'rate'} onclick={() => (tab = 'rate')}>Rate limits</button>
  <button class:active={tab === 'mirror'} onclick={() => (tab = 'mirror')}>Port mirroring</button>
  <button class:active={tab === 'preview'} onclick={() => (tab = 'preview')}>Generated rules</button>
</div>

{#if tab === 'acl'}
  <div class="notice info" style="margin-bottom:12px">Rules are evaluated top to bottom; the first match wins, then the ACL's default applies. Only <b>untagged IP traffic</b> is filtered (tagged frames on trunks pass). Management is never filtered.</div>
  <div class="stack">
    {#each acls as a, ai}
      <div class="card">
        <div class="card-head">
          <div class="row">
            <input bind:value={a.name} oninput={() => (dirty = true)} style="width:140px" aria-label="ACL name" disabled={!isAdmin()} />
            <select bind:value={a.port} onchange={() => (dirty = true)} style="width:auto" disabled={!isAdmin()}><option value="">all ports</option>{#each ports as p}<option value={p}>{p}</option>{/each}</select>
            <select bind:value={a.direction} onchange={() => (dirty = true)} style="width:auto" disabled={!isAdmin()}><option value="in">in (into the switch)</option><option value="out">out (out of the port)</option></select>
            <span class="small muted">default</span>
            <select bind:value={a.default} onchange={() => (dirty = true)} style="width:auto" disabled={!isAdmin()}><option value="pass">pass</option><option value="block">block</option></select>
          </div>
          {#if isAdmin()}<button class="sm danger" onclick={() => { acls.splice(ai, 1); dirty = true }}>Delete ACL</button>{/if}
        </div>
        <div class="card-body flush table-wrap">
          <table>
            <thead><tr><th>#</th><th>Action</th><th>Protocol</th><th>Source</th><th>Destination</th><th>Dst port</th><th>Comment</th><th></th></tr></thead>
            <tbody>
              {#each a.rules as r, i}
                <tr>
                  <td>{i + 1}</td>
                  <td><select bind:value={r.action} onchange={() => (dirty = true)} disabled={!isAdmin()}><option value="pass">pass</option><option value="block">block</option></select></td>
                  <td><select bind:value={r.proto} onchange={() => (dirty = true)} disabled={!isAdmin()}><option value="any">any</option><option value="tcp">tcp</option><option value="udp">udp</option><option value="icmp">icmp</option><option value="icmp6">icmp6</option></select></td>
                  <td><input bind:value={r.src} placeholder="any" oninput={() => (dirty = true)} disabled={!isAdmin()} /></td>
                  <td><input bind:value={r.dst} placeholder="any" oninput={() => (dirty = true)} disabled={!isAdmin()} /></td>
                  <td><input bind:value={r.dst_port} placeholder={r.proto === 'tcp' || r.proto === 'udp' ? '443 or 1000:2000' : '—'} disabled={!isAdmin() || !(r.proto === 'tcp' || r.proto === 'udp')} oninput={() => (dirty = true)} style="width:110px" /></td>
                  <td><input bind:value={r.comment} oninput={() => (dirty = true)} disabled={!isAdmin()} /></td>
                  <td class="nowrap">{#if isAdmin()}<button class="sm ghost" disabled={i === 0} onclick={() => move(a, i, -1)} aria-label="up">↑</button><button class="sm ghost" disabled={i === a.rules.length - 1} onclick={() => move(a, i, 1)} aria-label="down">↓</button><button class="sm ghost" onclick={() => { a.rules.splice(i, 1); dirty = true }} aria-label="delete">✕</button>{/if}</td>
                </tr>
              {/each}
              <tr><td colspan="8">{#if isAdmin()}<button class="sm" onclick={() => addRule(a)}>Add rule</button>{/if} <span class="small faint">then: <b>{a.default}</b> everything else</span></td></tr>
            </tbody>
          </table>
        </div>
      </div>
    {:else}<div class="card"><div class="empty">No ACLs: all traffic is switched without filtering (pf is off).</div></div>{/each}
    {#if isAdmin()}<div class="row"><button onclick={addACL}>Add ACL</button><button class="primary" disabled={!dirty} onclick={stageACLs}>Stage ACLs</button></div>{/if}
  </div>
{:else if tab === 'rate'}
  <div class="card" style="max-width:760px">
    <div class="card-body flush table-wrap">
      <table>
        <thead><tr><th>Port</th><th>Ingress limit (kbit/s)</th><th>Egress limit (kbit/s)</th></tr></thead>
        <tbody>{#each rates as r}<tr><td><b>{r.port}</b></td><td><input type="number" min="0" bind:value={r.ingress_kbps} disabled={!isAdmin()} /></td><td><input type="number" min="0" bind:value={r.egress_kbps} disabled={!isAdmin()} /></td></tr>{/each}</tbody>
      </table>
    </div>
  </div>
  <p class="small faint">0 = unlimited. Implemented with dummynet pipes via pf (software shaping: adds latency and CPU load; not a hardware policer). Can be used as a rough storm-control substitute.</p>
  {#if isAdmin()}<button class="primary" onclick={stageRates}>Stage rate limits</button>{/if}
{:else if tab === 'mirror'}
  <div class="card" style="max-width:760px">
    <div class="card-body stack">
      <div class="notice warn"><b>Limitation:</b> if_bridge span ports receive a copy of <b>all</b> frames forwarded by the bridge (every port, every VLAN, both directions). Per-source-port, per-direction or per-VLAN mirroring (as on hardware switches) is not possible. The destination must be an unused interface, not a switch port.</div>
      {#each mirrorCandidates as i}
        <label class="check"><input type="checkbox" checked={mirrors.includes(i)} disabled={!isAdmin()} onchange={(e) => (mirrors = e.currentTarget.checked ? [...mirrors, i] : mirrors.filter((x) => x !== i))} /> {i} as mirror destination</label>
      {:else}<div class="faint">No unused interface available. Mark one as "unused" on the Interfaces page.</div>{/each}
      {#if isAdmin() && mirrorCandidates.length}<div><button class="primary" onclick={stageMirrors}>Stage mirroring</button></div>{/if}
      <div class="small faint">Alternative for one port: System › Diagnostics › Packet capture (tcpdump on that port, pcap download).</div>
    </div>
  </div>
{:else if preview}
  <div class="stack">
    {#each preview.notes as n}<div class="notice small">{n}</div>{/each}
    <div class="card"><div class="card-head"><h2>pf.conf (staged configuration)</h2>{#if !preview.enabled}<span class="badge">pf disabled</span>{/if}</div>
      <div class="card-body"><pre data-testid="pf-preview">{preview.pf_conf || '# no ACLs or rate limits: pf is not used'}</pre></div></div>
    {#if preview.dnctl.length}<div class="card"><div class="card-head"><h2>dummynet pipes</h2></div><div class="card-body"><pre>{preview.dnctl.join('\n')}</pre></div></div>{/if}
  </div>
{/if}
