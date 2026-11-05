<script>
  import { app, navigate } from '../lib/store.svelte.js'
  import General from './system/General.svelte'
  import Management from './system/Management.svelte'
  import Users from './system/Users.svelte'
  import Tokens from './system/Tokens.svelte'
  import Certificate from './system/Certificate.svelte'
  import Config from './system/Config.svelte'
  import Logs from './system/Logs.svelte'
  import Diagnostics from './system/Diagnostics.svelte'
  import Power from './system/Power.svelte'
  import About from './system/About.svelte'
  import Audit from './system/Audit.svelte'

  const tabs = [
    ['general', 'General', General],
    ['management', 'Management access', Management],
    ['users', 'Users', Users],
    ['tokens', 'API tokens', Tokens],
    ['certificate', 'Certificate', Certificate],
    ['config', 'Configuration', Config],
    ['logs', 'Logs', Logs],
    ['audit', 'Audit log', Audit],
    ['diagnostics', 'Diagnostics', Diagnostics],
    ['power', 'Power', Power],
    ['about', 'About', About],
  ]
  const tab = $derived(app.query.get('tab') || 'general')
  const Current = $derived((tabs.find((t) => t[0] === tab) || tabs[0])[2])
</script>

<div class="page-head"><div><h1>System</h1></div></div>
<div class="tabs" role="tablist">
  {#each tabs as [id, title]}
    <button class:active={tab === id} role="tab" onclick={() => navigate('/system?tab=' + id, true)}>{title}</button>
  {/each}
</div>
{#key tab}<Current />{/key}
