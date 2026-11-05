<script>
  // Lightweight canvas time-series chart (no dependencies).
  import { bps as fmtBps } from '../lib/format.js'
  let {
    samples = [],
    series = [
      { key: 'rx_bps', label: 'RX', color: '--rx' },
      { key: 'tx_bps', label: 'TX', color: '--tx' },
    ],
    format = fmtBps,
    height = 180,
    window: win = 300, // seconds shown
  } = $props()

  let canvas = $state()
  let wrap = $state()
  let width = $state(600)
  let hover = $state(null)

  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || '#888'
  }

  function niceMax(v) {
    if (v <= 0) return 1
    const p = Math.pow(10, Math.floor(Math.log10(v)))
    for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p
    return 10 * p
  }

  function visible() {
    if (!samples.length) return []
    const last = samples[samples.length - 1].t
    return samples.filter((s) => s.t > last - win)
  }

  function draw() {
    if (!canvas) return
    const dpr = window.devicePixelRatio || 1
    canvas.width = width * dpr
    canvas.height = height * dpr
    const ctx = canvas.getContext('2d')
    ctx.scale(dpr, dpr)
    ctx.clearRect(0, 0, width, height)
    const pad = { l: 64, r: 10, t: 10, b: 22 }
    const w = width - pad.l - pad.r, h = height - pad.t - pad.b
    const data = visible()
    const now = data.length ? data[data.length - 1].t : Math.floor(Date.now() / 1000)
    const t0 = now - win
    let max = 0
    for (const s of data) for (const sr of series) max = Math.max(max, s[sr.key] || 0)
    max = niceMax(max * 1.1)
    ctx.font = '11px ' + cssVar('--font')
    ctx.fillStyle = cssVar('--text-3')
    ctx.strokeStyle = cssVar('--grid')
    ctx.lineWidth = 1
    for (let i = 0; i <= 4; i++) {
      const y = pad.t + h - (h * i) / 4
      ctx.beginPath(); ctx.moveTo(pad.l, y + .5); ctx.lineTo(pad.l + w, y + .5); ctx.stroke()
      ctx.textAlign = 'right'; ctx.textBaseline = 'middle'
      ctx.fillText(format((max * i) / 4), pad.l - 8, y)
    }
    ctx.textAlign = 'center'; ctx.textBaseline = 'top'
    const step = win <= 300 ? 60 : win <= 900 ? 180 : 600
    for (let t = Math.ceil(t0 / step) * step; t <= now; t += step) {
      const x = pad.l + ((t - t0) / win) * w
      const d = new Date(t * 1000)
      ctx.fillText(d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }), x, pad.t + h + 6)
    }
    for (const sr of series) {
      const col = cssVar(sr.color)
      ctx.strokeStyle = col
      ctx.lineWidth = 1.75
      ctx.beginPath()
      let first = true
      for (const s of data) {
        const x = pad.l + ((s.t - t0) / win) * w
        const y = pad.t + h - ((s[sr.key] || 0) / max) * h
        if (first) { ctx.moveTo(x, y); first = false } else ctx.lineTo(x, y)
      }
      ctx.stroke()
      // soft area fill
      if (data.length > 1) {
        ctx.lineTo(pad.l + ((data[data.length - 1].t - t0) / win) * w, pad.t + h)
        ctx.lineTo(pad.l + ((data[0].t - t0) / win) * w, pad.t + h)
        ctx.closePath()
        ctx.globalAlpha = 0.08
        ctx.fillStyle = col
        ctx.fill()
        ctx.globalAlpha = 1
      }
    }
    if (hover && data.length) {
      const t = t0 + ((hover.x - pad.l) / w) * win
      let best = data[0]
      for (const s of data) if (Math.abs(s.t - t) < Math.abs(best.t - t)) best = s
      const x = pad.l + ((best.t - t0) / win) * w
      ctx.strokeStyle = cssVar('--border-strong')
      ctx.beginPath(); ctx.moveTo(x + .5, pad.t); ctx.lineTo(x + .5, pad.t + h); ctx.stroke()
      hover.sample = best
    }
  }

  $effect(() => {
    // track dependencies
    samples.length; samples[samples.length - 1]; width; height; hover?.x; win
    draw()
  })

  $effect(() => {
    if (!wrap) return
    const ro = new ResizeObserver(() => { width = Math.max(200, wrap.clientWidth) })
    ro.observe(wrap)
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const redraw = () => draw()
    mq.addEventListener('change', redraw)
    const mo = new MutationObserver(redraw)
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => { ro.disconnect(); mq.removeEventListener('change', redraw); mo.disconnect() }
  })

  const last = $derived(samples.length ? samples[samples.length - 1] : null)
  const shown = $derived(hover?.sample || last)
</script>

<div class="chart" bind:this={wrap}>
  <div class="legend">
    {#each series as s}
      <span class="item"><i style="background: var({s.color})"></i>{s.label} <b>{shown ? format(shown[s.key] || 0) : '—'}</b></span>
    {/each}
    {#if hover?.sample}<span class="faint small">{new Date(hover.sample.t * 1000).toLocaleTimeString()}</span>{/if}
  </div>
  <canvas
    bind:this={canvas}
    style="width:{width}px;height:{height}px"
    onmousemove={(e) => (hover = { x: e.offsetX })}
    onmouseleave={() => (hover = null)}
    aria-label="throughput chart"
    role="img"
  ></canvas>
</div>

<style>
  .chart { width: 100%; }
  .legend { display: flex; gap: 16px; align-items: center; margin-bottom: 6px; font-size: 12.5px; color: var(--text-2); flex-wrap: wrap; }
  .item { display: inline-flex; align-items: center; gap: 6px; }
  .item i { width: 10px; height: 10px; border-radius: 2px; display: inline-block; }
  .item b { color: var(--text); font-variant-numeric: tabular-nums; font-weight: 600; }
  canvas { display: block; }
</style>
