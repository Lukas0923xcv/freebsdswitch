<script>
  import { post } from '../lib/api.js'
  import { app, afterLogin, toast } from '../lib/store.svelte.js'
  let { forced = false, ondone } = $props()
  let old = $state('')
  let pw = $state('')
  let pw2 = $state('')
  let error = $state('')
  let busy = $state(false)
  const weak = $derived(pw.length > 0 && pw.length < 8)
  async function submit(e) {
    e.preventDefault()
    error = ''
    if (pw !== pw2) { error = 'The new passwords do not match.'; return }
    busy = true
    try {
      await post('/auth/password', { old_password: old, new_password: pw })
      toast('Password changed', 'ok')
      if (forced) { app.me = { ...app.me, must_change: false }; await afterLogin() }
      old = pw = pw2 = ''
      ondone?.()
    } catch (err) { error = err.message }
    busy = false
  }
</script>

<div class={forced ? 'wrap' : ''}>
  <form class="card" onsubmit={submit} style="max-width:420px;width:100%">
    <div class="card-head"><h2>{forced ? 'Choose a new password' : 'Change password'}</h2></div>
    <div class="card-body stack">
      {#if forced}<div class="notice info">You are logged in with a one-time password. Choose your own password to continue.</div>{/if}
      <div class="field"><label for="o">Current password</label><input id="o" type="password" autocomplete="current-password" bind:value={old} required /></div>
      <div class="field"><label for="n">New password</label><input id="n" type="password" autocomplete="new-password" bind:value={pw} class:invalid={weak} required minlength="8" maxlength="72" />
        <div class="hint">At least 8 characters.</div></div>
      <div class="field"><label for="n2">Repeat new password</label><input id="n2" type="password" autocomplete="new-password" bind:value={pw2} required /></div>
      {#if error}<div class="notice bad" role="alert">{error}</div>{/if}
      <button class="primary" type="submit" disabled={busy || weak}>Change password</button>
    </div>
  </form>
</div>

<style>
  .wrap { min-height: 100vh; display: grid; place-items: center; padding: 16px; }
</style>
