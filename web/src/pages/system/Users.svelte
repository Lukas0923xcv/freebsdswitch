<script>
  import { get, post, patch, del } from '../../lib/api.js'
  import { app, isAdmin, toast, errorToast } from '../../lib/store.svelte.js'
  import { time } from '../../lib/format.js'
  import Modal from '../../components/Modal.svelte'
  import ChangePassword from '../ChangePassword.svelte'
  let users = $state([])
  let adding = $state(null)
  let resetting = $state(null)
  async function load() { if (isAdmin()) users = await get('/users') }
  $effect(() => { load().catch(errorToast) })
  async function add() {
    try { await post('/users', adding); toast(`User ${adding.name} created`, 'ok'); adding = null; load() } catch (e) { errorToast(e) }
  }
  async function role(u, r) {
    try { await patch('/users/' + u.name, { role: r }); toast('Role changed', 'ok'); load() } catch (e) { errorToast(e); load() }
  }
  async function reset() {
    try { await patch('/users/' + resetting.name, { password: resetting.password, must_change: true }); toast('Password set; user must change it at next login', 'ok'); resetting = null } catch (e) { errorToast(e) }
  }
  async function remove(u) {
    if (!confirm(`Delete user ${u.name}?`)) return
    try { await del('/users/' + u.name); load() } catch (e) { errorToast(e) }
  }
</script>

<div class="grid cols-2">
  {#if isAdmin()}
    <div class="card">
      <div class="card-head"><h2>Users</h2><button class="sm primary" onclick={() => (adding = { name: '', role: 'readonly', password: '', must_change: true })}>Add user</button></div>
      <div class="card-body flush table-wrap">
        <table>
          <thead><tr><th>User</th><th>Role</th><th>Password changed</th><th></th></tr></thead>
          <tbody>
            {#each users as u (u.name)}
              <tr>
                <td><b>{u.name}</b>{#if u.must_change}<span class="badge warn">must change</span>{/if}</td>
                <td><select value={u.role} onchange={(e) => role(u, e.currentTarget.value)} style="width:auto" disabled={u.name === app.me.user}><option value="admin">admin</option><option value="readonly">read-only</option></select></td>
                <td class="small">{time(u.changed)}</td>
                <td class="right nowrap"><button class="sm" onclick={() => (resetting = { name: u.name, password: '' })}>Set password</button>{#if u.name !== app.me.user}<button class="sm danger" onclick={() => remove(u)}>Delete</button>{/if}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {/if}
  <ChangePassword />
</div>
<p class="small faint">Roles: <b>admin</b> can change everything; <b>read-only</b> can view all pages but not stage, apply or save changes. Passwords are stored as bcrypt hashes.</p>

{#if adding}
  <Modal title="Add user" onclose={() => (adding = null)}>
    <div class="stack">
      <div class="field"><label for="un">User name</label><input id="un" bind:value={adding.name} pattern="[a-z_][a-z0-9_.\-]*" /></div>
      <div class="field"><label for="ur">Role</label><select id="ur" bind:value={adding.role}><option value="readonly">read-only</option><option value="admin">admin</option></select></div>
      <div class="field"><label for="up">Initial password</label><input id="up" type="password" bind:value={adding.password} minlength="8" autocomplete="new-password" /></div>
      <label class="check"><input type="checkbox" bind:checked={adding.must_change} /> Must change password at first login</label>
    </div>
    {#snippet footer()}<button onclick={() => (adding = null)}>Cancel</button><button class="primary" onclick={add}>Create</button>{/snippet}
  </Modal>
{/if}
{#if resetting}
  <Modal title="Set password for {resetting.name}" onclose={() => (resetting = null)}>
    <div class="field"><label for="rp">New password</label><input id="rp" type="password" bind:value={resetting.password} minlength="8" autocomplete="new-password" /></div>
    {#snippet footer()}<button onclick={() => (resetting = null)}>Cancel</button><button class="primary" onclick={reset}>Set password</button>{/snippet}
  </Modal>
{/if}
