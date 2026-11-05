<script>
  import { get, post, del } from '../../lib/api.js'
  import { isAdmin, toast, errorToast } from '../../lib/store.svelte.js'
  import { time } from '../../lib/format.js'
  let tokens = $state([])
  let name = $state('')
  let role = $state('readonly')
  let ttl = $state(0)
  let created = $state(null)
  async function load() { tokens = await get('/tokens' + (isAdmin() ? '?all=1' : '')) }
  $effect(() => { load().catch(errorToast) })
  async function create(e) {
    e.preventDefault()
    try { created = await post('/tokens', { name, role, ttl_days: Number(ttl) }); name = ''; load() } catch (err) { errorToast(err) }
  }
  async function remove(t) {
    if (!confirm(`Revoke token "${t.name}"?`)) return
    try { await del('/tokens/' + t.id); load() } catch (e) { errorToast(e) }
  }
</script>

<div class="grid cols-2">
  <div class="card">
    <div class="card-head"><h2>API tokens</h2></div>
    <div class="card-body flush table-wrap">
      <table>
        <thead><tr><th>Name</th><th>User</th><th>Role</th><th>Created</th><th>Last used</th><th>Expires</th><th></th></tr></thead>
        <tbody>
          {#each tokens as t (t.id)}
            <tr><td><b>{t.name}</b></td><td>{t.user}</td><td>{t.role}</td><td class="small">{time(t.created)}</td><td class="small">{t.last_used?.startsWith('0001') ? 'never' : time(t.last_used)}</td><td class="small">{t.expires?.startsWith('0001') ? 'never' : time(t.expires)}</td>
              <td><button class="sm danger" onclick={() => remove(t)}>Revoke</button></td></tr>
          {:else}<tr><td colspan="7" class="empty">No tokens</td></tr>{/each}
        </tbody>
      </table>
    </div>
  </div>
  <form class="card" onsubmit={create}>
    <div class="card-head"><h2>Create token</h2></div>
    <div class="card-body stack">
      <div class="field"><label for="tn">Name</label><input id="tn" bind:value={name} required maxlength="40" placeholder="e.g. monitoring" /></div>
      <div class="field"><label for="tr">Role</label><select id="tr" bind:value={role}><option value="readonly">read-only</option>{#if isAdmin()}<option value="admin">admin</option>{/if}</select></div>
      <div class="field"><label for="tt">Expires after (days, 0 = never)</label><input id="tt" type="number" min="0" bind:value={ttl} /></div>
      <button class="primary" type="submit">Create</button>
      {#if created}
        <div class="notice info"><b>Copy this token now</b> – it is not shown again:<pre style="margin-top:6px;white-space:pre-wrap;word-break:break-all">{created.token}</pre>
          <div class="small">Use it as <code>Authorization: Bearer …</code> or <code>bsdswitchctl -H https://switch -t TOKEN</code>.</div></div>
      {/if}
    </div>
  </form>
</div>
