<script>
  import Modal from './Modal.svelte'
  import Chart from './Chart.svelte'
  import { get, post } from '../lib/api.js'
  import { app, toast, errorToast, isAdmin } from '../lib/store.svelte.js'
  import { count, bytes, pps } from '../lib/format.js'
  let { port, onclose } = $props()
  let hist = $state([])
  let counters = $state(port.counters)
  let mode = $state('bps')
  let range = $state(300)
  async function load() {
    try {
      const r = await get(`/ports/${encodeURIComponent(port.name)}/stats?since=${Math.floor(Date.now() / 1000 - 3600)}`)
      hist = r.history
      counters = r.counters
    } catch (e) { errorToast(e) }
  }
  load()
  $effect(() => {
    const s = app.live?.ports?.[port.name]
    if (s && s.t && (!hist.length || hist[hist.length - 1].t < s.t)) hist.push(s)
  })
  $effect(() => { const t = setInterval(async () => { try { counters = (await get(`/ports/${encodeURIComponent(port.name)}/stats?since=${Math.floor(Date.now() / 1000)}`)).counters } catch {} }, 3000); return () => clearInterval(t) })
  async function reset() {
    try { await post(`/ports/${encodeURIComponent(port.name)}/reset-counters`); toast('Counters reset', 'ok'); load() } catch (e) { errorToast(e) }
  }
  const series = $derived(mode === 'bps'
    ? [{ key: 'rx_bps', label: 'RX', color: '--rx' }, { key: 'tx_bps', label: 'TX', color: '--tx' }]
    : [{ key: 'rx_pps', label: 'RX', color: '--rx' }, { key: 'tx_pps', label: 'TX', color: '--tx' }])
</script>

<Modal title="Traffic – {port.name}{port.description ? ' (' + port.description + ')' : ''}" wide {onclose}>
  <div class="row between" style="margin-bottom:8px">
    <div class="row">
      <select style="width:auto" bind:value={mode}><option value="bps">bits/s</option><option value="pps">packets/s</option></select>
      <select style="width:auto" bind:value={range}><option value={300}>5 min</option><option value={900}>15 min</option><option value={3600}>1 hour</option></select>
    </div>
    <span class="small faint">last hour kept in RAM, 1 s resolution</span>
  </div>
  {#key mode}<Chart samples={hist} {series} window={range} format={mode === 'bps' ? undefined : pps} height={240} />{/key}
  <div class="table-wrap" style="margin-top:16px">
    <table>
      <thead><tr><th></th><th class="num">Packets</th><th class="num">Bytes</th><th class="num">Errors</th><th class="num">Drops</th><th class="num">Multicast</th></tr></thead>
      <tbody>
        <tr><td>RX</td><td class="num">{count(counters.rx_packets)}</td><td class="num">{bytes(counters.rx_bytes)}</td><td class="num">{count(counters.rx_errors)}</td><td class="num">{count(counters.rx_drops)}</td><td class="num">{count(counters.rx_multicast)}</td></tr>
        <tr><td>TX</td><td class="num">{count(counters.tx_packets)}</td><td class="num">{bytes(counters.tx_bytes)}</td><td class="num">{count(counters.tx_errors)}</td><td class="num">{count(counters.tx_drops)}</td><td class="num">{count(counters.tx_multicast)}</td></tr>
      </tbody>
    </table>
  </div>
  {#snippet footer()}
    {#if isAdmin()}<button onclick={reset}>Reset counters</button>{/if}
    <button onclick={onclose}>Close</button>
  {/snippet}
</Modal>
