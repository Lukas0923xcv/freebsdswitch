<script>
  import { get, post, put, api } from '../../lib/api.js'
  import { app, isAdmin, toast, errorToast, refreshStatus, bump } from '../../lib/store.svelte.js'
  import { time } from '../../lib/format.js'
  import Diff from '../../components/Diff.svelte'
  import Modal from '../../components/Modal.svelte'

  let view = $state('running')
  let yaml = $state('')
  let compare = $state('')
  let history = $state([])
  let editor = $state(null)
  let viewing = $state(null)

  async function text(path) { return (await api('GET', path, null, { raw: true })).text() }
  async function load() {
    try {
      yaml = await text(`/config/${view}?format=yaml`).catch(() => '# no startup configuration saved yet')
      compare = (await get('/config/compare?a=startup&b=running')).diff
      history = await get('/config/history')
    } catch (e) { errorToast(e) }
  }
  $effect(() => { view; app.version; load() })

  async function save() {
    try { await post('/config/save'); toast('Saved as startup configuration', 'ok'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }
  function download(which) {
    const a = document.createElement('a')
    a.href = `/api/v1/config/export?which=${which}`
    a.click()
  }
  async function restore(e) {
    const f = e.currentTarget.files[0]
    if (!f) return
    try { await post('/config/import', await f.text()); toast('Backup loaded as staged changes. Review and apply.', 'ok'); await refreshStatus(); bump() } catch (err) { errorToast(err) }
    e.currentTarget.value = ''
  }
  async function openEditor() { editor = await text('/config/candidate?format=yaml') }
  async function stageYaml() {
    try { await put('/config/candidate', editor); editor = null; toast('YAML staged', 'ok'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }
  async function viewVersion(v) {
    const y = await text(`/config/history/${v.id}?format=yaml`)
    const d = (await get(`/config/compare?a=${v.id}&b=running`)).diff
    viewing = { v, y, d }
  }
  async function stageRollback(v) {
    try { await post('/config/rollback/' + v.id); viewing = null; toast(`Version ${v.id} staged. Review and apply.`, 'ok'); await refreshStatus(); bump() } catch (e) { errorToast(e) }
  }
</script>

<div class="stack" style="gap:16px">
  <div class="card">
    <div class="card-head">
      <h2>Running vs. startup</h2>
      <div class="row">
        {#if app.status?.startup_differs}<span class="badge warn">differ</span>{:else}<span class="badge ok">identical</span>{/if}
        {#if isAdmin()}<button class="primary sm" onclick={save} disabled={!app.status?.startup_differs || !!app.status?.pending}>Save running → startup</button>{/if}
      </div>
    </div>
    <div class="card-body"><Diff text={compare} /></div>
  </div>

  <div class="grid cols-2">
    <div class="card">
      <div class="card-head"><h2>Backup &amp; restore</h2></div>
      <div class="card-body stack">
        <div class="row"><button onclick={() => download('running')}>Download running config</button><button onclick={() => download('startup')}>Download startup config</button></div>
        {#if isAdmin()}
          <div class="field"><label for="rf">Restore from backup (loaded as staged changes)</label><input id="rf" type="file" accept=".yaml,.yml" onchange={restore} /></div>
          <div><button onclick={openEditor}>Edit configuration as YAML…</button></div>
        {/if}
        <div class="small faint">Backups contain the switch configuration only; user accounts and the TLS key are not included.</div>
      </div>
    </div>
    <div class="card">
      <div class="card-head"><h2>History (last 10)</h2></div>
      <div class="card-body flush table-wrap">
        <table>
          <thead><tr><th>#</th><th>Time</th><th>User</th><th>Comment</th><th></th></tr></thead>
          <tbody>
            {#each history as v (v.id)}
              <tr><td>{v.index}{#if v.index === 0} <span class="badge ok">current</span>{/if}</td><td class="small">{time(v.time)}</td><td>{v.user}</td><td class="small">{v.comment}</td>
                <td class="right"><button class="sm" onclick={() => viewVersion(v)}>View</button></td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  </div>

  <div class="card">
    <div class="card-head">
      <h2>Configuration file</h2>
      <select style="width:auto" bind:value={view}><option value="running">running</option><option value="startup">startup</option><option value="candidate">candidate (staged)</option></select>
    </div>
    <div class="card-body"><pre style="max-height:60vh">{yaml}</pre></div>
  </div>
</div>

{#if editor !== null}
  <Modal title="Edit configuration (YAML)" wide onclose={() => (editor = null)}>
    <p class="small muted" style="margin-top:0">The edited file replaces the staged configuration after validation. Nothing is applied until you click Apply.</p>
    <textarea rows="28" bind:value={editor} spellcheck="false"></textarea>
    {#snippet footer()}<button onclick={() => (editor = null)}>Cancel</button><button class="primary" onclick={stageYaml}>Validate &amp; stage</button>{/snippet}
  </Modal>
{/if}
{#if viewing}
  <Modal title="Version {viewing.v.id} – {time(viewing.v.time)}" wide onclose={() => (viewing = null)}>
    <h3 style="margin-bottom:6px">Difference to the running configuration</h3>
    <Diff text={viewing.d} />
    <details style="margin-top:12px"><summary>Full YAML</summary><pre style="max-height:40vh">{viewing.y}</pre></details>
    {#snippet footer()}
      <button onclick={() => (viewing = null)}>Close</button>
      {#if isAdmin() && viewing.v.index !== 0}<button class="primary" onclick={() => stageRollback(viewing.v)}>Roll back to this version…</button>{/if}
    {/snippet}
  </Modal>
{/if}
