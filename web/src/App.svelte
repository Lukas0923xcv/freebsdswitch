<script>
  import { app, loadSession, logout, navigate, isAdmin } from './lib/store.svelte.js'
  import Icon from './components/Icon.svelte'
  import Toasts from './components/Toasts.svelte'
  import ChangesBar from './components/ChangesBar.svelte'
  import Login from './pages/Login.svelte'
  import ChangePassword from './pages/ChangePassword.svelte'
  import { routes, nav } from './routes.js'

  loadSession()

  let theme = $state(document.documentElement.dataset.theme || '')
  function toggleTheme() {
    const dark = theme ? theme === 'dark' : window.matchMedia('(prefers-color-scheme: dark)').matches
    theme = dark ? 'light' : 'dark'
    document.documentElement.dataset.theme = theme
    try { localStorage.setItem('bsdswitch-theme', theme) } catch {}
  }
  const isDark = $derived(theme ? theme === 'dark' : window.matchMedia('(prefers-color-scheme: dark)').matches)

  const current = $derived(routes.find((r) => r.path === app.path) || routes.find((r) => app.path.startsWith(r.path + '/')) || routes[0])

  function link(e, path) {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return
    e.preventDefault()
    navigate(path)
  }
  $effect(() => { document.title = `${current.title} · ${app.status?.hostname || 'bsdswitch'}` })
</script>

{#if !app.ready}
  <div class="empty">Loading…</div>
{:else if !app.me}
  <Login />
{:else if app.me.must_change}
  <ChangePassword forced />
{:else}
  <div class="shell" class:open={app.navOpen}>
    <aside class="side">
      <a class="logo" href="/" onclick={(e) => link(e, '/')}>
        <svg viewBox="0 0 32 32" width="26" height="26"><rect x="1" y="7" width="30" height="18" rx="3" fill="var(--accent)"/><g fill="#fff"><rect x="5" y="12" width="4" height="4" rx=".6"/><rect x="11" y="12" width="4" height="4" rx=".6"/><rect x="17" y="12" width="4" height="4" rx=".6"/><rect x="23" y="12" width="4" height="4" rx=".6"/></g></svg>
        <span>bsdswitch</span>
      </a>
      <nav aria-label="main">
        {#each nav as group}
          {#if group.title}<div class="group">{group.title}</div>{/if}
          {#each group.items as r}
            <a href={r.path} class:active={current.path === r.path} onclick={(e) => link(e, r.path)}>
              <Icon name={r.icon} />{r.title}
            </a>
          {/each}
        {/each}
      </nav>
    </aside>
    <div class="scrim" role="presentation" onclick={() => (app.navOpen = false)}></div>
    <div class="main">
      <header class="top">
        <button class="ghost icon menu" onclick={() => (app.navOpen = !app.navOpen)} aria-label="Menu"><Icon name="menu" /></button>
        <div class="crumb">{current.title}</div>
        <div class="grow"></div>
        <span class="badge {isAdmin() ? 'info' : ''}" title="role">{app.me.user} · {app.me.role}</span>
        <button class="ghost icon" onclick={toggleTheme} aria-label="Toggle dark mode" title="Toggle dark mode"><Icon name={isDark ? 'sun' : 'moon'} /></button>
        <button class="ghost icon" onclick={logout} aria-label="Log out" title="Log out"><Icon name="logout" /></button>
      </header>
      <ChangesBar />
      <main class="content">
        {#key current.path}
          <current.component />
        {/key}
      </main>
    </div>
  </div>
{/if}
<Toasts />

<style>
  .shell { display: grid; grid-template-columns: 232px minmax(0, 1fr); min-height: 100vh; }
  .side { background: var(--surface); border-right: 1px solid var(--border); position: sticky; top: 0; height: 100vh; overflow-y: auto; padding: 12px 10px; }
  .logo { display: flex; align-items: center; gap: 10px; padding: 6px 8px 14px; color: var(--text); font-weight: 700; font-size: 16px; text-decoration: none; }
  nav a { display: flex; align-items: center; gap: 10px; padding: 7px 10px; border-radius: 6px; color: var(--text-2); text-decoration: none; margin: 1px 0; }
  nav a:hover { background: var(--surface-3); color: var(--text); }
  nav a.active { background: var(--accent-soft); color: var(--accent); font-weight: 600; }
  .group { font-size: 11px; text-transform: uppercase; letter-spacing: .06em; color: var(--text-3); padding: 14px 10px 4px; }
  .main { min-width: 0; display: flex; flex-direction: column; }
  .top { display: flex; align-items: center; gap: 8px; height: 52px; padding: 0 16px; border-bottom: 1px solid var(--border); background: var(--surface); position: sticky; top: 0; z-index: 20; }
  .crumb { font-weight: 600; }
  .content { padding: 20px; max-width: 1480px; width: 100%; }
  .menu { display: none; }
  .scrim { display: none; }
  @media (max-width: 900px) {
    .shell { grid-template-columns: minmax(0, 1fr); }
    .side { position: fixed; z-index: 60; left: 0; top: 0; width: 248px; transform: translateX(-100%); transition: transform .18s; box-shadow: var(--shadow-lg); }
    .shell.open .side { transform: none; }
    .shell.open .scrim { display: block; position: fixed; inset: 0; background: rgba(0, 0, 0, .35); z-index: 50; }
    .menu { display: inline-flex; }
    .content { padding: 14px; }
  }
</style>
