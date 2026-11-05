import Dashboard from './pages/Dashboard.svelte'
import Interfaces from './pages/Interfaces.svelte'
import Ports from './pages/Ports.svelte'
import Vlans from './pages/Vlans.svelte'
import MacTable from './pages/MacTable.svelte'
import Stp from './pages/Stp.svelte'
import System from './pages/System.svelte'

export const routes = [
  { path: '/', title: 'Dashboard', icon: 'dashboard', component: Dashboard },
  { path: '/interfaces', title: 'Interfaces', icon: 'interfaces', component: Interfaces },
  { path: '/ports', title: 'Ports', icon: 'ports', component: Ports },
  { path: '/vlans', title: 'VLANs', icon: 'vlan', component: Vlans },
  { path: '/mac', title: 'MAC table', icon: 'mac', component: MacTable },
  { path: '/stp', title: 'Spanning tree', icon: 'stp', component: Stp },
  { path: '/system', title: 'System', icon: 'system', component: System },
]

const by = (p) => routes.find((r) => r.path === p)
export const nav = [
  { title: '', items: ['/', '/interfaces', '/ports', '/vlans'].map(by) },
  { title: 'Switching', items: ['/mac', '/stp'].map(by) },
  { title: 'Administration', items: ['/system'].map(by) },
]
