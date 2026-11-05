<script>
  import { post, setCSRF } from '../lib/api.js'
  import { app, afterLogin } from '../lib/store.svelte.js'
  let username = $state('admin')
  let password = $state('')
  let error = $state('')
  let busy = $state(false)
  async function submit(e) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      const me = await post('/auth/login', { username, password })
      setCSRF(me.csrf)
      app.me = me
      if (!me.must_change) await afterLogin()
    } catch (err) {
      error = err.status === 429 ? 'Too many failed attempts. Wait a few minutes and try again.' : err.message
    }
    busy = false
  }
</script>

<div class="wrap">
  <form class="card" onsubmit={submit}>
    <div class="card-body stack">
      <div class="brand">
        <svg viewBox="0 0 32 32" width="40" height="40"><rect x="1" y="7" width="30" height="18" rx="3" fill="var(--accent)"/><g fill="#fff"><rect x="5" y="12" width="4" height="4" rx=".6"/><rect x="11" y="12" width="4" height="4" rx=".6"/><rect x="17" y="12" width="4" height="4" rx=".6"/><rect x="23" y="12" width="4" height="4" rx=".6"/></g></svg>
        <div><h1>bsdswitch</h1><div class="muted small">Managed switch on FreeBSD</div></div>
      </div>
      <div class="field"><label for="u">Username</label><input id="u" name="username" autocomplete="username" bind:value={username} required /></div>
      <div class="field"><label for="p">Password</label><input id="p" name="password" type="password" autocomplete="current-password" bind:value={password} required /></div>
      {#if error}<div class="notice bad" role="alert">{error}</div>{/if}
      <button class="primary" type="submit" disabled={busy}>Log in</button>
      <p class="small faint" style="margin:0">First login: use the one-time password shown on the switch console (menu header). Lost it? Console option 3 resets it.</p>
    </div>
  </form>
</div>

<style>
  .wrap { min-height: 100vh; display: grid; place-items: center; padding: 16px; }
  form { width: 100%; max-width: 380px; }
  .brand { display: flex; gap: 12px; align-items: center; margin-bottom: 4px; }
</style>
