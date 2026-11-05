// Minimal REST client for /api/v1 with CSRF handling.
let csrf = ''
let onUnauthorized = () => {}

export function setCSRF(t) { csrf = t || '' }
export function setUnauthorizedHandler(f) { onUnauthorized = f }

export class ApiError extends Error {
  constructor(status, body) {
    super(body?.error || `HTTP ${status}`)
    this.status = status
    this.fields = body?.fields || []
  }
  // human readable, including field errors
  get detail() {
    if (!this.fields.length) return this.message
    return this.message + ': ' + this.fields.map((f) => `${f.path}: ${f.message}`).join('; ')
  }
}

export async function api(method, path, body, opts = {}) {
  const headers = {}
  let payload
  if (body !== undefined && body !== null) {
    if (typeof body === 'string') {
      headers['Content-Type'] = 'application/yaml'
      payload = body
    } else {
      headers['Content-Type'] = 'application/json'
      payload = JSON.stringify(body)
    }
  }
  if (method !== 'GET' && csrf) headers['X-CSRF-Token'] = csrf
  const res = await fetch('/api/v1' + path, { method, headers, body: payload, credentials: 'same-origin' })
  if (opts.raw) {
    if (!res.ok) throw new ApiError(res.status, await res.json().catch(() => ({})))
    return res
  }
  const text = await res.text()
  let data = null
  try { data = text ? JSON.parse(text) : null } catch { data = text }
  if (res.status === 401 && !path.startsWith('/auth/login')) onUnauthorized()
  if (!res.ok) throw new ApiError(res.status, data)
  return data
}

export const get = (p) => api('GET', p)
export const post = (p, b) => api('POST', p, b ?? {})
export const patch = (p, b) => api('PATCH', p, b)
export const put = (p, b) => api('PUT', p, b)
export const del = (p) => api('DELETE', p)
