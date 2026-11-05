<script>
  import { get, post, api } from '../../lib/api.js'
  import { toast, errorToast } from '../../lib/store.svelte.js'
  let st = $state(null)
  let busy = $state(false)
  async function load() { st = await get('/system/update') }
  load().catch(errorToast)
  async function upload(e) {
    const f = e.currentTarget.files[0]
    if (!f) return
    busy = true
    try {
      const res = await fetch('/api/v1/system/update/upload?name=' + encodeURIComponent(f.name), { method: 'POST', body: f, headers: { 'X-CSRF-Token': (await get('/auth/me')).csrf } })
      if (!res.ok) throw new Error((await res.json()).error)
      st = await res.json()
      toast('Package uploaded', 'ok')
    } catch (err) { errorToast(err) }
    busy = false
  }
  async function check() { busy = true; try { st = await post('/system/update/check') } catch (e) { errorToast(e) } busy = false }
  async function apply(source) {
    if (!confirm('Install the update now? bsdswitchd restarts afterwards; switching continues.')) return
    busy = true
    try { const r = await post('/system/update/apply', { source }); toast(r.message, 'ok', 10000); setTimeout(() => location.reload(), 8000) } catch (e) { errorToast(e) }
    busy = false
  }
  async function revert() {
    if (!confirm(`Reboot into the boot environment ${st.last_be} (the system as it was before the last update)?`)) return
    try { await post('/system/update/revert', { confirm: true }); toast('Rebooting…', 'warn', 10000) } catch (e) { errorToast(e) }
  }
</script>

{#if st}
<div class="grid cols-2">
  <div class="card"><div class="card-head"><h2>Installed</h2></div><div class="card-body stack">
    <dl class="kv"><dt>bsdswitch</dt><dd>{st.current}</dd>
      <dt>Boot environments</dt><dd>{#if st.boot_environments}<span class="badge ok">available (ZFS)</span>{:else}<span class="badge">not available (UFS)</span>{/if}</dd>
      {#if st.repo_version}<dt>Repository</dt><dd>{st.repo_version}</dd>{/if}
      {#if st.last_be}<dt>Pre-update BE</dt><dd class="mono small">{st.last_be}</dd>{/if}</dl>
    {#if st.message}<div class="notice small">{st.message}</div>{/if}
    <div class="row"><button onclick={check} disabled={busy}>Check repository</button>{#if st.repo_version && st.repo_version !== st.current}<button class="primary" onclick={() => apply('repo')} disabled={busy}>Upgrade from repository</button>{/if}</div>
    {#if st.last_be}<div><button class="danger" onclick={revert}>Revert: reboot into {st.last_be}</button></div>{/if}
  </div></div>
  <div class="card"><div class="card-head"><h2>Upload a package</h2></div><div class="card-body stack">
    <div class="field"><label for="uf">bsdswitch-&lt;version&gt;.pkg</label><input id="uf" type="file" accept=".pkg" onchange={upload} disabled={busy} /></div>
    {#if st.uploaded}<div class="row"><span class="mono">{st.uploaded}</span><button class="primary" onclick={() => apply('upload')} disabled={busy}>Install</button></div>{/if}
    <div class="small faint">On ZFS a boot environment is created before the update, so you can always boot the previous version (also from the loader menu). Only upload packages you built or trust: they run as root.</div>
  </div></div>
</div>
{/if}
