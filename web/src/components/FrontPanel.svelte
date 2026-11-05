<script>
  // Switch faceplate: management port on the left, switch ports in two
  // rows (odd numbers on top, like most rack switches).
  import { speed } from '../lib/format.js'
  let { ports = [], mgmt = null, hostname = '', onselect } = $props()

  function state(p) {
    if (!p.exists) return 'missing'
    if (p.shutdown || !p.admin_up) return 'disabled'
    return p.link_up ? 'up' : 'down'
  }
  function stpClass(p) {
    const s = p.stp?.state
    if (!s || s === 'forwarding') return ''
    if (s === 'discarding' || s === 'blocking' || s === 'disabled') return 'blocking'
    return 'learning'
  }
  function title(p) {
    const st = state(p)
    let t = `${p.name}${p.description ? ' – ' + p.description : ''}\n${st}`
    if (p.speed && p.speed !== 'n/a') t += `, ${speed(p.speed)} ${p.duplex}`
    t += `\n${p.mode === 'trunk' ? `trunk, native ${p.native || 'none'}, tagged ${p.tagged?.join(',') || '–'}` : `access VLAN ${p.pvid}`}`
    if (p.stp?.state) t += `\nSTP ${p.stp.role || ''} ${p.stp.state}`
    if (p.is_lag) t += `\nLAG: ${p.lag_members.join(', ')}`
    return t
  }
  const top = $derived(ports.filter((_, i) => i % 2 === 0))
  const bottom = $derived(ports.filter((_, i) => i % 2 === 1))
</script>

<div class="chassis" role="group" aria-label="switch front panel">
  <div class="brand">
    <b>bsdswitch</b>
    <span>{hostname}</span>
  </div>
  {#if mgmt}
    <div class="mgmt">
      <button class="port {mgmt.link_up ? 'up' : 'down'}" title="Management: {mgmt.interface}" onclick={() => onselect?.('__mgmt')}>
        <span class="jack"></span><span class="led"></span>
      </button>
      <span class="lbl">MGMT</span>
      <span class="sub">{mgmt.interface}</span>
    </div>
  {/if}
  <div class="bank">
    {#each [top, bottom] as row, r}
      <div class="row-ports">
        {#each row as p, i (p.name)}
          {@const n = i * 2 + r + 1}
          <div class="slot" class:bottom={r === 1}>
            {#if r === 0}<span class="num">{n}</span>{/if}
            <button
              class="port {state(p)} {stpClass(p)}"
              class:trunk={p.mode === 'trunk'}
              class:staged={p.staged}
              title={title(p)}
              aria-label="{p.name} {state(p)}"
              data-port={p.name}
              onclick={() => onselect?.(p.name)}
            >
              <span class="jack"></span><span class="led"></span>
            </button>
            <div class="badges">
              <span class="b">{p.is_lag ? 'LAG' : speed(p.speed)}</span>
              <span class="b mode">{p.mode === 'trunk' ? 'T' : p.pvid}</span>
            </div>
            {#if r === 1}<span class="num">{n}</span>{/if}
          </div>
        {/each}
      </div>
    {/each}
  </div>
</div>
<div class="legend small muted">
  <span><i class="sw up"></i>link up</span>
  <span><i class="sw down"></i>no link</span>
  <span><i class="sw disabled"></i>disabled</span>
  <span><i class="sw blk"></i>STP blocking</span>
  <span><b>T</b> trunk / number = access VLAN</span>
</div>

<style>
  .chassis {
    display: flex; align-items: stretch; gap: 18px;
    background: linear-gradient(180deg, var(--chassis), var(--chassis-2));
    border-radius: 10px; padding: 14px 18px; overflow-x: auto;
    box-shadow: inset 0 1px 0 rgba(255, 255, 255, .06), var(--shadow);
    color: #cbd5e1;
  }
  .brand { display: flex; flex-direction: column; justify-content: center; min-width: 92px; }
  .brand b { font-size: 13px; letter-spacing: .04em; color: #e2e8f0; }
  .brand span { font-size: 11px; color: #94a3b8; overflow: hidden; text-overflow: ellipsis; max-width: 120px; white-space: nowrap; }
  .mgmt { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 3px; padding-right: 18px; border-right: 1px solid rgba(255, 255, 255, .08); }
  .mgmt .lbl { font-size: 10px; font-weight: 700; letter-spacing: .08em; color: #fbbf24; }
  .mgmt .sub { font-size: 10px; color: #94a3b8; }
  .bank { display: flex; flex-direction: column; gap: 6px; }
  .row-ports { display: flex; gap: 8px; }
  .slot { display: flex; flex-direction: column; align-items: center; gap: 2px; width: 44px; }
  .num { font-size: 10px; color: #94a3b8; height: 12px; }
  .port {
    position: relative; width: 40px; height: 32px; padding: 0;
    border-radius: 4px; border: 2px solid #0b1220;
    background: var(--port-down); cursor: pointer;
  }
  .port:hover { filter: brightness(1.15); background: var(--port-down); }
  .port.up, .port.up:hover { background: var(--port-up); }
  .port.disabled, .port.disabled:hover { background: var(--port-disabled); }
  .port.missing { background: repeating-linear-gradient(45deg, #3b4656, #3b4656 4px, #2a3442 4px, #2a3442 8px); }
  .port.blocking { border-color: #f59e0b; box-shadow: 0 0 0 1px #f59e0b; }
  .port.learning { border-color: #fde68a; }
  .port.staged::after { content: ''; position: absolute; top: -5px; right: -5px; width: 9px; height: 9px; border-radius: 50%; background: #60a5fa; border: 2px solid var(--chassis); }
  .jack { position: absolute; left: 8px; right: 8px; top: 7px; bottom: 5px; background: rgba(0, 0, 0, .55); border-radius: 2px; clip-path: polygon(0 0, 100% 0, 100% 70%, 75% 70%, 75% 100%, 25% 100%, 25% 70%, 0 70%); }
  .led { position: absolute; top: 2px; left: 3px; width: 5px; height: 3px; border-radius: 1px; background: rgba(0, 0, 0, .4); }
  .port.up .led { background: #bbf7d0; box-shadow: 0 0 4px #86efac; }
  .port.trunk .led { left: auto; right: 3px; background: #fcd34d; }
  .badges { display: flex; gap: 2px; }
  .b { font-size: 9.5px; line-height: 13px; padding: 0 3px; border-radius: 3px; background: rgba(255, 255, 255, .08); color: #cbd5e1; font-variant-numeric: tabular-nums; }
  .b.mode { background: rgba(96, 165, 250, .18); color: #bfdbfe; }
  .slot.bottom .badges { order: -1; }
  .legend { display: flex; gap: 16px; flex-wrap: wrap; margin-top: 8px; align-items: center; }
  .legend span { display: inline-flex; gap: 6px; align-items: center; }
  .sw { width: 12px; height: 10px; border-radius: 2px; display: inline-block; background: var(--port-down); }
  .sw.up { background: var(--port-up); }
  .sw.disabled { background: var(--port-disabled); }
  .sw.blk { background: var(--port-down); outline: 2px solid #f59e0b; }
</style>
