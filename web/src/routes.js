import Dashboard from './pages/Dashboard.svelte'
import Interfaces from './pages/Interfaces.svelte'
import Ports from './pages/Ports.svelte'
import Vlans from './pages/Vlans.svelte'
import MacTable from './pages/MacTable.svelte'
import Stp from './pages/Stp.svelte'
import System from './pages/System.svelte'
import Lag from './pages/Lag.svelte'
import Security from './pages/Security.svelte'
import Lldp from './pages/Lldp.svelte'

export const routes = [
  { path: '/', title: 'Dashboard', icon: 'dashboard', component: Dashboard },
  { path: '/interfaces', title: 'Interfaces', icon: 'interfaces', component: Interfaces },
  { path: '/ports', title: 'Ports', icon: 'ports', component: Ports },
  { path: '/vlans', title: 'VLANs', icon: 'vlan', component: Vlans },
  { path: '/mac', title: 'MAC table', icon: 'mac', component: MacTable },
  { path: '/stp', title: 'Spanning tree', icon: 'stp', component: Stp },
  { path: '/lag', title: 'Link aggregation', icon: 'lag', component: Lag },
  { path: '/security', title: 'ACL / QoS / Mirror', icon: 'acl', component: Security },
  { path: '/lldp', title: 'LLDP neighbors', icon: 'lldp', component: Lldp },
  { path: '/system', title: 'System', icon: 'system', component: System },
]

const by = (p) => routes.find((r) => r.path === p)
export const nav = [
  { title: '', items: ['/', '/interfaces', '/ports', '/vlans'].map(by) },
  { title: 'Switching', items: ['/mac', '/stp', '/lag', '/security', '/lldp'].map(by) },
  { title: 'Administration', items: ['/system'].map(by) },
]
