<script>
  import { post } from '../../lib/api.js'
  import { app, isAdmin, toast, errorToast } from '../../lib/store.svelte.js'
  async function act(path, msg, body) {
    if (!confirm(msg)) return
    try { await post(path, body); toast('Requested. The web UI will reconnect when the switch is back.', 'warn', 10000) } catch (e) { errorToast(e) }
  }
</script>

{#if !isAdmin()}<div class="notice">Power actions require the admin role.</div>{:else}
<div class="grid cols-3">
  <div class="card"><div class="card-body stack"><h2>Restart daemon</h2><p class="muted" style="margin:0">Restarts bsdswitchd. Forwarding is not interrupted (it is done by the kernel).</p><div><button onclick={() => act('/system/restart', 'Restart bsdswitchd?')}>Restart bsdswitchd</button></div></div></div>
  <div class="card"><div class="card-body stack"><h2>Reboot</h2><p class="muted" style="margin:0">The startup configuration is applied at boot.{#if app.status?.startup_differs} <b>Unsaved running changes will be lost.</b>{/if}</p><div><button class="danger" onclick={() => act('/system/reboot', 'Reboot the switch now?', { confirm: true })}>Reboot</button></div></div></div>
  <div class="card"><div class="card-body stack"><h2>Shut down</h2><p class="muted" style="margin:0">Powers the switch off. You need console or physical access to start it again.</p><div><button class="danger solid" onclick={() => act('/system/shutdown', 'Shut the switch down? You will need physical access to power it on.', { confirm: true })}>Shut down</button></div></div></div>
</div>
{/if}
