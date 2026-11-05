<script>
  import { untrack } from 'svelte'
  import Modal from './Modal.svelte'
  import { patch } from '../lib/api.js'
  import { vlanList, parseVlanList } from '../lib/format.js'
  import { toast, errorToast, refreshStatus, bump, isAdmin } from '../lib/store.svelte.js'
  let { port, config, vlans = [], onclose } = $props()

  // local editable copy (initial values only)
  const config0 = untrack(() => config)
  let f = $state({
    description: config0.description || '',
    shutdown: !!config0.shutdown,
    speed: config0.speed || 'auto',
    duplex: config0.duplex || 'auto',
    mode: config0.mode,
    pvid: config0.pvid || 1,
    native: config0.native || 0,
    tagged: vlanList(config0.tagged),
    stpEnabled: config0.stp?.enabled ?? true,
    edge: !!config0.stp?.edge,
    autoEdge: config0.stp?.auto_edge ?? true,
    priority: config0.stp?.priority || 128,
    cost: config0.stp?.path_cost || 0,
    maxAddr: config0.max_addresses || 0,
    isolated: !!config0.isolated,
    qinq: !!config0.qinq,
    vlanProto: config0.vlan_proto || '802.1q',
  })
  let errors = $state({})
  let busy = $state(false)

  const vlanIds = $derived(vlans.map((v) => v.id))
  function validate() {
    const e = {}
    if (f.description.length > 64 || /["'`$;|&<>\\]/.test(f.description)) e.description = 'max 64 characters, no quotes or shell characters'
    if (f.mode === 'access' && !vlanIds.includes(Number(f.pvid))) e.pvid = `VLAN ${f.pvid} is not defined`
    if (f.mode === 'trunk') {
      try {
        const t = parseVlanList(f.tagged)
        const missing = t.filter((v) => !vlanIds.includes(v))
        if (missing.length) e.tagged = `not defined: ${vlanList(missing)}`
        if (Number(f.native) && t.includes(Number(f.native))) e.tagged = 'the native VLAN cannot also be tagged'
        if (!Number(f.native) && !t.length) e.tagged = 'a trunk needs a native or tagged VLAN'
      } catch (err) { e.tagged = err.message }
      if (Number(f.native) && !vlanIds.includes(Number(f.native))) e.native = `VLAN ${f.native} is not defined`
    }
    if (f.priority % 16 || f.priority < 0 || f.priority > 240) e.priority = '0-240 in steps of 16'
    if (f.cost < 0 || f.cost > 200000000) e.cost = '0 (auto) to 200000000'
    if (f.maxAddr < 0 || f.maxAddr > 65535) e.maxAddr = '0-65535'
    if (f.speed !== 'auto' && f.duplex === 'half' && Number(f.speed) > 100) e.duplex = 'half duplex only at 10/100'
    errors = e
    return !Object.keys(e).length
  }
  $effect(() => { f.mode; f.pvid; f.tagged; f.native; f.priority; f.cost; f.maxAddr; f.description; f.duplex; validate() })

  async function save() {
    if (!validate()) return
    busy = true
    const body = {
      description: f.description, shutdown: f.shutdown, mode: f.mode,
      stp: { enabled: f.stpEnabled, edge: f.edge, auto_edge: f.autoEdge, priority: Number(f.priority), path_cost: Number(f.cost) },
      max_addresses: Number(f.maxAddr), isolated: f.isolated,
      qinq: f.qinq, vlan_proto: f.vlanProto === '802.1q' ? '' : f.vlanProto,
    }
    if (port.speed_settable) { body.speed = f.speed; body.duplex = f.speed === 'auto' ? 'auto' : f.duplex }
    if (f.mode === 'access') body.pvid = Number(f.pvid)
    else { body.native = Number(f.native); body.tagged = parseVlanList(f.tagged); body.pvid = 0 }
    try {
      await patch('/ports/' + encodeURIComponent(port.name), body)
      toast(`${port.name}: changes staged`, 'ok')
      await refreshStatus(); bump()
      onclose()
    } catch (e) { errorToast(e) }
    busy = false
  }
</script>

<Modal title="Port {port.name}{port.description ? ' – ' + port.description : ''}" {onclose}>
  <form class="form-grid" onsubmit={(e) => { e.preventDefault(); save() }} id="port-form">
    <div class="field full"><label for="pd">Description</label><input id="pd" bind:value={f.description} class:invalid={errors.description} maxlength="64" />{#if errors.description}<div class="err">{errors.description}</div>{/if}</div>
    <div class="field"><label class="check"><input type="checkbox" checked={!f.shutdown} onchange={(e) => (f.shutdown = !e.currentTarget.checked)} /> Port enabled</label></div>
    <div class="field"><label class="check"><input type="checkbox" bind:checked={f.isolated} /> Protected (isolated) port</label><div class="hint">Isolated ports cannot talk to each other.</div></div>

    <div class="field"><label for="ps">Speed</label>
      {#if port.speed_settable}
        <select id="ps" bind:value={f.speed}>{#each port.speed_options as s}<option value={s}>{s === 'auto' ? 'Auto' : s >= 1000 ? s / 1000 + ' Gbit/s' : s + ' Mbit/s'}</option>{/each}</select>
      {:else}<input id="ps" value="n/a (not supported by {port.driver || 'this NIC'})" disabled />{/if}
    </div>
    <div class="field"><label for="pdx">Duplex</label>
      <select id="pdx" bind:value={f.duplex} disabled={!port.speed_settable || f.speed === 'auto'} class:invalid={errors.duplex}>
        <option value="auto">Auto</option><option value="full">Full</option><option value="half">Half</option>
      </select>{#if errors.duplex}<div class="err">{errors.duplex}</div>{/if}
    </div>

    <div class="field"><label for="pm">VLAN mode</label>
      <select id="pm" bind:value={f.mode} data-testid="port-mode"><option value="access">Access (untagged)</option><option value="trunk">Trunk (802.1Q tagged)</option></select></div>
    {#if f.mode === 'access'}
      <div class="field"><label for="pv">Access VLAN (PVID)</label>
        <select id="pv" bind:value={f.pvid} class:invalid={errors.pvid} data-testid="port-pvid">{#each vlans as v}<option value={v.id}>{v.id} – {v.name}</option>{/each}</select>
        {#if errors.pvid}<div class="err">{errors.pvid}</div>{/if}</div>
    {:else}
      <div class="field"><label for="pn">Native VLAN (untagged)</label>
        <select id="pn" bind:value={f.native} class:invalid={errors.native}><option value={0}>none (tagged only)</option>{#each vlans as v}<option value={v.id}>{v.id} – {v.name}</option>{/each}</select>
        {#if errors.native}<div class="err">{errors.native}</div>{/if}</div>
      <div class="field full"><label for="pt">Tagged VLANs</label><input id="pt" bind:value={f.tagged} placeholder="10,20,30-39" class:invalid={errors.tagged} data-testid="port-tagged" />
        {#if errors.tagged}<div class="err">{errors.tagged}</div>{:else}<div class="hint">Comma separated list or ranges.</div>{/if}</div>
    {/if}

    <div class="field full"><hr style="margin:4px 0" /><b>Spanning tree</b></div>
    <div class="field"><label class="check"><input type="checkbox" bind:checked={f.stpEnabled} /> Participate in STP/RSTP</label></div>
    <div class="field"><label class="check"><input type="checkbox" bind:checked={f.edge} /> Edge port (PortFast)</label><label class="check" style="margin-top:6px"><input type="checkbox" bind:checked={f.autoEdge} /> Auto-detect edge</label></div>
    <div class="field"><label for="pp">Port priority</label><input id="pp" type="number" min="0" max="240" step="16" bind:value={f.priority} class:invalid={errors.priority} />{#if errors.priority}<div class="err">{errors.priority}</div>{/if}</div>
    <div class="field"><label for="pc">Path cost</label><input id="pc" type="number" min="0" bind:value={f.cost} class:invalid={errors.cost} /><div class="hint">0 = automatic (from link speed)</div></div>
    <div class="field full"><details><summary class="small">Advanced: Q-in-Q (802.1ad)</summary>
      <div class="form-grid" style="margin-top:8px">
        <div class="field"><label for="vp">Tag protocol (TPID)</label><select id="vp" bind:value={f.vlanProto}><option value="802.1q">802.1Q (0x8100)</option><option value="802.1ad">802.1ad (0x88a8, S-tag)</option></select>
          <div class="hint">802.1ad on provider/uplink ports carrying S-tags.</div></div>
        <div class="field"><label class="check"><input type="checkbox" bind:checked={f.qinq} /> Allow Q-in-Q frames on this port</label>
          <div class="hint">Customer port that may send already-tagged frames into an S-VLAN. No selective QinQ.</div></div>
      </div></details></div>
    <div class="field"><label for="pa">Max MAC addresses (port security)</label><input id="pa" type="number" min="0" bind:value={f.maxAddr} class:invalid={errors.maxAddr} /><div class="hint">0 = unlimited</div></div>
  </form>
  {#snippet footer()}
    <button onclick={onclose}>Cancel</button>
    {#if isAdmin()}<button class="primary" type="submit" form="port-form" disabled={busy || Object.keys(errors).length > 0} data-testid="port-save">Stage changes</button>{/if}
  {/snippet}
</Modal>
