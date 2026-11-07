import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const routes: RouteRecordRaw[] = [
  { path: '/login', name: 'login', component: () => import('../views/LoginView.vue'), meta: { public: true, title: '登录' } },
  { path: '/', name: 'dashboard', component: () => import('../views/DashboardView.vue'), meta: { title: '仪表盘' } },
  { path: '/devices', name: 'devices', component: () => import('../views/DevicesView.vue'), meta: { title: '设备管理' } },
  { path: '/agents', name: 'agents', component: () => import('../views/AgentsView.vue'), meta: { title: 'Agent 节点' } },
  { path: '/tasks', name: 'tasks', component: () => import('../views/TasksView.vue'), meta: { title: '定时任务' } },
  { path: '/scan', name: 'scan', component: () => import('../views/ScanView.vue'), meta: { title: '局域网扫描' } },
  { path: '/logs', name: 'logs', component: () => import('../views/LogsView.vue'), meta: { title: '操作日志' } },
  { path: '/settings', name: 'settings', component: () => import('../views/SettingsView.vue'), meta: { title: '设置' } },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (to.meta.public) return true
  if (!auth.checked) await auth.restore()
  if (!auth.me) return { name: 'login' }
  return true
})

router.afterEach((to) => {
  const title = (to.meta.title as string) ?? 'WakeHub'
  document.title = `${title} - WakeHub`
})

export const NAV_ITEMS = [
  { name: 'dashboard', label: '仪表盘', icon: '🏠', path: '/' },
  { name: 'devices', label: '设备', icon: '🖥️', path: '/devices' },
  { name: 'scan', label: '扫描', icon: '🔍', path: '/scan' },
  { name: 'tasks', label: '任务', icon: '⏰', path: '/tasks' },
  { name: 'agents', label: '节点', icon: '📡', path: '/agents' },
  { name: 'logs', label: '日志', icon: '📋', path: '/logs' },
  { name: 'settings', label: '设置', icon: '⚙️', path: '/settings' },
] as const

/** 移动端底部导航只放 5 项，其余收进“更多”。 */
export const MOBILE_NAV = [
  NAV_ITEMS[0],
  NAV_ITEMS[1],
  NAV_ITEMS[2],
  NAV_ITEMS[3],
  { name: 'more', label: '更多', icon: '⋯', path: '' },
] as const
