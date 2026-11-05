<script>
  import { get } from '../../lib/api.js'
  import { errorToast } from '../../lib/store.svelte.js'
  let src = $state('daemon')
  let lines = $state([])
  let syslog = $state([])
  let follow = $state(true)
  let box = $state()
  let filter = $state('')
  async function load() {
    try {
      const r = await get('/logs?n=1000')
      lines = r.daemon.map((l) => `${new Date(l.time).toLocaleString()}  ${l.text}`)
      syslog = r.syslog
    } catch (e) { errorToast(e) }
  }
  load()
  $effect(() => {
    const es = new EventSource('/api/v1/events?topics=logs')
    es.addEventListener('log', (e) => {
      const l = JSON.parse(e.data)
      lines.push(`${new Date(l.time).toLocaleString()}  ${l.text}`)
      if (lines.length > 2000) lines.splice(0, lines.length - 2000)
    })
    const t = setInterval(async () => { if (src === 'syslog') { try { syslog = (await get('/logs?n=1000')).syslog } catch {} } }, 5000)
    return () => { es.close(); clearInterval(t) }
  })
  const shown = $derived((src === 'daemon' ? lines : syslog).filter((l) => !filter || l.toLowerCase().includes(filter.toLowerCase())))
  $effect(() => { shown.length; if (follow && box) queueMicrotask(() => (box.scrollTop = box.scrollHeight)) })
</script>

<div class="card">
  <div class="card-head">
    <div class="row">
      <select style="width:auto" bind:value={src}><option value="daemon">bsdswitchd (live)</option><option value="syslog">/var/log/messages</option></select>
      <input placeholder="Filter…" bind:value={filter} style="width:200px" />
    </div>
    <label class="check"><input type="checkbox" bind:checked={follow} /> Follow</label>
  </div>
  <div class="card-body"><pre bind:this={box} style="height:65vh">{shown.join('\n')}</pre></div>
</div>
