export function bps(v) {
  if (v == null || isNaN(v)) return '—'
  const u = ['bps', 'kbps', 'Mbps', 'Gbps', 'Tbps']
  let i = 0
  while (v >= 1000 && i < u.length - 1) { v /= 1000; i++ }
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${u[i]}`
}
export function pps(v) {
  if (v == null || isNaN(v)) return '—'
  if (v >= 1e6) return (v / 1e6).toFixed(1) + ' Mpps'
  if (v >= 1e3) return (v / 1e3).toFixed(1) + ' kpps'
  return v.toFixed(0) + ' pps'
}
export function bytes(v) {
  if (v == null) return '—'
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${i === 0 ? v : v.toFixed(1)} ${u[i]}`
}
export function count(v) {
  if (v == null) return '—'
  return Number(v).toLocaleString()
}
export function duration(sec) {
  sec = Math.max(0, Math.floor(sec))
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60)
  if (d) return `${d}d ${h}h ${m}m`
  if (h) return `${h}h ${m}m`
  if (m) return `${m}m ${sec % 60}s`
  return `${sec}s`
}
export function speed(s) {
  if (!s || s === 'n/a') return 'n/a'
  const n = Number(s)
  if (isNaN(n)) return s
  return n >= 1000 ? `${n / 1000}G` : `${n}M`
}
export function time(t) {
  if (!t) return '—'
  const d = new Date(t)
  return d.toLocaleString()
}
export function vlanList(list) {
  if (!list || !list.length) return ''
  const s = [...list].sort((a, b) => a - b)
  const out = []
  for (let i = 0; i < s.length; ) {
    let j = i
    while (j + 1 < s.length && s[j + 1] === s[j] + 1) j++
    out.push(j === i ? `${s[i]}` : `${s[i]}-${s[j]}`)
    i = j + 1
  }
  return out.join(',')
}
export function parseVlanList(str) {
  const out = new Set()
  for (const part of String(str || '').split(',').map((x) => x.trim()).filter(Boolean)) {
    const m = part.match(/^(\d+)(?:-(\d+))?$/)
    if (!m) throw new Error(`invalid VLAN list item "${part}"`)
    const a = +m[1], b = m[2] ? +m[2] : a
    if (a < 1 || b > 4094 || a > b) throw new Error(`VLAN out of range in "${part}" (1-4094)`)
    for (let v = a; v <= b; v++) out.add(v)
  }
  return [...out].sort((a, b) => a - b)
}
