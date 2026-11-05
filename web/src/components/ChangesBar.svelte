<script>
  // Pending (staged) changes bar, review dialog, apply (commit-confirm)
  // dialog, commit countdown banner and "not saved" indicator.
  import Modal from './Modal.svelte'
  import Diff from './Diff.svelte'
  import Icon from './Icon.svelte'
  import { app, isAdmin, toast, errorToast, refreshStatus, bump } from '../lib/store.svelte.js'
  import { get, post, del } from '../lib/api.js'

  let preview = $state(null)
  let showReview = $state(false)
  let showApply = $state(false)
  let timeout = $state(60)
  let comment = $state('')
  let busy = $state(false)
  let now = $state(Date.now())

  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 250)
    return () => clearInterval(t)
  })

  // reload the preview whenever staged config changes
  $effect(() => {
    app.version
    if (app.status?.staged) loadPreview()
    else preview = null
  })

  async function loadPreview() {
    try { preview = await get('/config/diff') } catch (e) { preview = null }
  }

  const changeCount = $derived(
    preview?.commands ? preview.commands.filter((c) => !c.startsWith('#')).length : 0
  )
  const pending = $derived(app.status?.pending)
  const remaining = $derived(pending ? Math.max(0, Math.ceil((pending.deadline_ms - (now + app.clockSkew)) / 1000)) : 0)
  const total = $derived(pending ? Math.max(1, Math.round((pending.deadline_ms - pending.started_ms) / 1000)) : 1)

  async function discard() {
    if (!confirm('Discard all staged changes?')) return
    try { await del('/config/candidate'); toast('Staged changes discarded'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }

  async function apply() {
    busy = true
    try {
      await post('/commit', { timeout: Number(timeout), comment })
      showApply = false
      showReview = false
      comment = ''
      toast(`Changes applied. Confirm within ${timeout}s or they will be rolled back.`, 'warn', 8000)
      await refreshStatus()
      bump()
    } catch (e) { errorToast(e) }
    busy = false
  }
  async function confirmCommit() {
    busy = true
    try { await post('/commit/confirm'); toast('Changes confirmed', 'ok'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
    busy = false
  }
  async function rollbackNow() {
    busy = true
    try { await post('/commit/cancel'); toast('Changes rolled back', 'warn'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
    busy = false
  }
  async function extend() {
    try { await post('/commit/extend', { timeout: 120 }); await refreshStatus() } catch (e) { errorToast(e) }
  }
  async function save() {
    busy = true
    try { await post('/config/save'); toast('Running configuration saved as startup configuration', 'ok'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
    busy = false
  }
</script>

{#if pending}
  <div class="bar commit" role="alert" data-testid="commit-banner">
    <div class="ring" style="--p:{remaining / total}"><span>{remaining}</span></div>
    <div class="grow">
      <b>Confirm the applied changes</b>
      <div class="small">Commit #{pending.id} by {pending.user} rolls back automatically in <b data-testid="countdown">{remaining}s</b> unless you confirm. If you lost access, just wait.</div>
    </div>
    {#if isAdmin()}
      <button class="sm" onclick={extend} disabled={busy}>+2 min</button>
      <button class="sm danger" onclick={rollbackNow} disabled={busy}>Roll back now</button>
      <button class="sm primary" onclick={confirmCommit} disabled={busy} data-testid="confirm-commit"><Icon name="check" size={14} />Confirm</button>
    {/if}
  </div>
{:else if app.status?.staged}
  <div class="bar staged" data-testid="pending-bar">
    <Icon name="edit" />
    <div class="grow">
      <b data-testid="pending-count">{changeCount} pending change{changeCount === 1 ? '' : 's'}</b>
      <span class="small muted">staged, not applied yet</span>
      {#if preview?.mgmt_impact?.length}<span class="badge warn" title={preview.mgmt_impact.join('\n')}><Icon name="warn" size={12} />affects management access</span>{/if}
    </div>
    <button class="sm" onclick={() => { showReview = true; loadPreview() }} data-testid="review">Review</button>
    {#if isAdmin()}
      <button class="sm danger" onclick={discard}>Discard</button>
      <button class="sm primary" onclick={() => (showApply = true)} data-testid="apply">Apply</button>
    {/if}
  </div>
{:else if app.status?.startup_differs}
  <div class="bar unsaved" data-testid="unsaved-bar">
    <Icon name="warn" />
    <div class="grow"><b>Running configuration is not saved.</b> <span class="small muted">It will be lost on reboot.</span></div>
    {#if isAdmin()}<button class="sm primary" onclick={save} disabled={busy} data-testid="save-config">Save</button>{/if}
  </div>
{/if}
{#if app.status?.boot_mode === 'safe'}
  <div class="bar safe" role="alert">
    <Icon name="warn" />
    <div class="grow"><b>Safe mode:</b> the startup configuration could not be applied at boot. Only the management interface is configured. Check System › Logs.</div>
  </div>
{/if}

{#if showReview}
  <Modal title="Review pending changes" wide onclose={() => (showReview = false)}>
    {#if !preview}<div class="empty">Loading…</div>{:else}
    {#if preview.mgmt_impact.length}
      <div class="notice warn" style="margin-bottom:12px">
        <b>These changes affect management access:</b>
        <ul>{#each preview.mgmt_impact as m}<li>{m}</li>{/each}</ul>
        Commit-confirm will roll them back automatically if you lose access.
      </div>
    {/if}
    {#each preview.warnings as w}<div class="notice" style="margin-bottom:8px">{w}</div>{/each}
    <h3 style="margin-bottom:6px">Configuration diff</h3>
    <Diff text={preview.diff} />
    <h3 style="margin:16px 0 6px">Commands that will run</h3>
    <pre data-testid="commands">{(preview.commands || []).join('\n') || '(no system commands – e.g. only a VLAN name changed)'}</pre>
    {/if}
    {#snippet footer()}
      <button onclick={() => (showReview = false)}>Close</button>
      {#if isAdmin()}<button class="primary" onclick={() => { showReview = false; showApply = true }}>Apply…</button>{/if}
    {/snippet}
  </Modal>
{/if}

{#if showApply}
  <Modal title="Apply changes" onclose={() => (showApply = false)}>
    <div class="stack">
      <p style="margin:0">The {changeCount} staged change{changeCount === 1 ? '' : 's'} will be applied now. You must <b>confirm</b> them within the timeout, otherwise the switch rolls back to the previous configuration automatically (even if this browser loses its connection).</p>
      {#if preview?.mgmt_impact?.length}<div class="notice warn">Management access may be interrupted: {preview.mgmt_impact.join('; ')}.</div>{/if}
      <div class="form-grid">
        <div class="field">
          <label for="ct">Confirm within</label>
          <select id="ct" bind:value={timeout} data-testid="commit-timeout">
            <option value={10}>10 seconds</option>
            <option value={30}>30 seconds</option>
            <option value={60}>60 seconds</option>
            <option value={120}>2 minutes</option>
            <option value={300}>5 minutes</option>
          </select>
        </div>
        <div class="field">
          <label for="cc">Comment (optional)</label>
          <input id="cc" bind:value={comment} maxlength="80" placeholder="e.g. move printer to VLAN 20" />
        </div>
      </div>
    </div>
    {#snippet footer()}
      <button onclick={() => (showApply = false)}>Cancel</button>
      <button class="primary" onclick={apply} disabled={busy} data-testid="apply-confirm">Apply with commit-confirm</button>
    {/snippet}
  </Modal>
{/if}

<style>
  .bar { display: flex; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid var(--border); flex-wrap: wrap; }
  .bar.staged { background: var(--info-soft); }
  .bar.unsaved { background: var(--warn-soft); }
  .bar.commit { background: var(--warn-soft); border-bottom-color: color-mix(in srgb, var(--warn) 40%, transparent); }
  .bar.safe { background: var(--bad-soft); }
  .bar b { margin-right: 6px; }
  .ring { --p: 1; width: 38px; height: 38px; border-radius: 50%; display: grid; place-items: center; background: conic-gradient(var(--warn) calc(var(--p) * 360deg), var(--border) 0); flex: none; }
  .ring span { width: 30px; height: 30px; border-radius: 50%; background: var(--surface); display: grid; place-items: center; font-size: 12px; font-weight: 700; font-variant-numeric: tabular-nums; }
  ul { margin: 6px 0; padding-left: 20px; }
</style>
