// Global reactive state shared by all pages (Svelte 5 runes).
import { get, post, setCSRF, setUnauthorizedHandler } from './api.js'

export const app = $state({
  me: null, // { user, role, must_change, session_timeout }
  ready: false,
  status: null, // config status: { startup_differs, staged, pending, boot_mode }
  system: null,
  live: { total: null, ports: {} }, // latest SSE stats sample
  history: [], // total throughput samples (dashboard)
  toasts: [],
  path: location.pathname,
  query: new URLSearchParams(location.search),
  version: 0, // bumped whenever config state changes -> pages reload
  linkVersion: 0,
  clockSkew: 0,
  navOpen: false,
})

export const isAdmin = () => app.me?.role === 'admin'

let toastId = 0
export function toast(msg, kind = 'info', ms = 4500) {
  const id = ++toastId
  app.toasts.push({ id, msg, kind })
  setTimeout(() => { app.toasts = app.toasts.filter((t) => t.id !== id) }, ms)
}
export function errorToast(e) { toast(e?.detail || e?.message || String(e), 'bad', 7000) }

export function navigate(to, replace = false) {
  if (to === location.pathname + location.search) return
  if (replace) history.replaceState({}, '', to)
  else history.pushState({}, '', to)
  app.path = location.pathname
  app.query = new URLSearchParams(location.search)
  app.navOpen = false
  window.scrollTo(0, 0)
}
window.addEventListener('popstate', () => {
  app.path = location.pathname
  app.query = new URLSearchParams(location.search)
})

export async function refreshStatus() {
  try {
    app.status = await get('/commit')
    if (app.status?.pending) app.clockSkew = app.status.pending.now_ms - Date.now()
  } catch {}
}

export function bump() { app.version++ }

setUnauthorizedHandler(() => {
  if (app.me) {
    app.me = null
    stopEvents()
    toast('Your session has ended. Please log in again.', 'warn')
  }
})

export async function loadSession() {
  try {
    const me = await get('/auth/me')
    setCSRF(me.csrf)
    app.me = me
    if (!me.must_change) await afterLogin()
  } catch {
    app.me = null
  }
  app.ready = true
}

export async function afterLogin() {
  await refreshStatus()
  startEvents()
}

export async function logout() {
  try { await post('/auth/logout') } catch {}
  stopEvents()
  app.me = null
  setCSRF('')
}

// ---- server-sent events --------------------------------------------------
let es = null
export function startEvents() {
  stopEvents()
  es = new EventSource('/api/v1/events?topics=stats,commit,link')
  es.addEventListener('hello', (e) => { app.status = JSON.parse(e.data) })
  es.addEventListener('stats', (e) => {
    const d = JSON.parse(e.data)
    app.live = d
    app.history.push(d.total)
    if (app.history.length > 3600) app.history.splice(0, app.history.length - 3600)
  })
  es.addEventListener('link', () => { app.linkVersion++ })
  es.addEventListener('commit', (e) => {
    const d = JSON.parse(e.data)
    app.status = d.status
    if (d.status?.pending) app.clockSkew = d.status.pending.now_ms - Date.now()
    const ev = d.event
    if (ev.type === 'auto-rollback') toast(ev.message, 'warn', 10000)
    else if (ev.type === 'apply-error') toast(ev.message, 'bad', 10000)
    bump()
  })
  es.addEventListener('logout', () => { app.me = null; stopEvents() })
}
export function stopEvents() {
  if (es) { es.close(); es = null }
}
