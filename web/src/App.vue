<template>
  <div class="app-shell min-h-screen">
    <Toast />
    <ConfirmDialog />

    <RouterView v-if="!auth.me" />

    <div v-else class="app-frame">
      <aside class="app-sidebar">
        <RouterLink to="/" class="brand-lockup" aria-label="WakeHub 首页">
          <span class="brand-mark"><span>⏻</span></span>
          <span class="brand-copy"><strong>WakeHub</strong><small>Network Control Center</small></span>
        </RouterLink>

        <div class="sidebar-section-label">工作空间</div>
        <nav class="sidebar-nav" aria-label="主导航">
          <RouterLink v-for="item in NAV_ITEMS" :key="item.name" :to="item.path" class="nav-link" :class="{ 'nav-link-active': route.name === item.name }">
            <span class="nav-icon" aria-hidden="true">{{ item.icon }}</span>
            <span class="nav-label">{{ item.label }}</span>
            <span v-if="item.name === 'agents' && agents.pendingCount() > 0" class="nav-count">{{ agents.pendingCount() }}</span>
          </RouterLink>
        </nav>

        <div class="sidebar-bottom">
          <div class="sidebar-health">
            <span class="health-dot" :class="healthOk ? 'is-online' : serverStatus === null ? 'is-unknown' : 'is-error'"></span>
            <span><strong>{{ healthText || '正在检查服务' }}</strong><small>Agent 在线 {{ agents.onlineCount() }} / {{ agents.items.length }}</small></span>
          </div>
          <div class="sidebar-user">
            <span class="user-avatar">{{ (auth.me.username || 'U').slice(0, 1).toUpperCase() }}</span>
            <span class="user-name">{{ auth.me.username }}</span>
            <button class="icon-button" aria-label="退出登录" title="退出登录" @click="logout">↗</button>
          </div>
        </div>
      </aside>

      <div class="app-main-column">
        <header class="app-topbar">
          <div class="topbar-title-wrap">
            <span class="mobile-brand-mark"><span>⏻</span></span>
            <strong class="mobile-brand-name">WakeHub</strong>
            <div class="topbar-heading"><span class="eyebrow">WAKEHUB CONTROL CENTER</span><h1>{{ pageTitle }}</h1></div>
          </div>
          <div class="topbar-actions">
            <span class="topbar-health"><span class="health-dot" :class="healthOk ? 'is-online' : serverStatus === null ? 'is-unknown' : 'is-error'"></span>{{ healthText || '检查中' }}<span v-if="uptimeSeconds !== null" class="health-uptime">· {{ formatUptime(uptimeSeconds) }}</span></span>
            <span v-if="uptimeSeconds !== null" class="uptime-label">运行 {{ formatUptime(uptimeSeconds) }}</span>
            <div class="theme-switch" role="group" aria-label="主题模式">
              <button :class="{ selected: ui.theme === 'light' }" aria-label="浅色主题" title="浅色主题" @click="ui.setTheme('light')">☀</button>
              <button :class="{ selected: ui.theme === 'system' }" aria-label="跟随系统" title="跟随系统" @click="ui.setTheme('system')">◐</button>
              <button :class="{ selected: ui.theme === 'dark' }" aria-label="深色主题" title="深色主题" @click="ui.setTheme('dark')">☾</button>
            </div>
            <div class="topbar-account"><span class="user-avatar">{{ (auth.me.username || 'U').slice(0, 1).toUpperCase() }}</span><span>{{ auth.me.username }}</span></div>
            <button class="mobile-more-trigger" aria-label="打开更多导航" @click="showMore = true">☰</button>
          </div>
        </header>

        <main class="app-content"><RouterView /></main>
        <footer class="app-footer"><span>WakeHub · Network Wake & Device Management</span><span v-if="uptimeSeconds !== null">服务运行正常</span></footer>
      </div>
    </div>

    <nav v-if="auth.me" class="mobile-bottom-nav" aria-label="移动端导航">
      <button v-for="item in MOBILE_NAV" :key="item.name" class="mobile-nav-item" :class="{ active: item.name !== 'more' && route.name === item.name }" @click="onMobileNav(item)">
        <span aria-hidden="true">{{ item.icon }}</span><small>{{ item.label }}</small>
      </button>
    </nav>

    <div v-if="showMore" class="mobile-drawer-backdrop" @click.self="showMore = false">
      <section class="mobile-more-drawer" role="dialog" aria-modal="true" aria-label="更多导航">
        <div class="drawer-handle"></div><div class="drawer-heading"><strong>更多功能</strong><button class="icon-button" aria-label="关闭" @click="showMore = false">✕</button></div>
        <RouterLink v-for="item in [NAV_ITEMS[4], NAV_ITEMS[5], NAV_ITEMS[6]]" :key="item.name" :to="item.path" class="drawer-link" @click="showMore = false">
          <span>{{ item.icon }}</span>{{ item.label }}<span v-if="item.name === 'agents' && agents.pendingCount() > 0" class="nav-count">{{ agents.pendingCount() }} 待审批</span><span class="drawer-arrow">›</span>
        </RouterLink>
        <div class="drawer-theme-row"><span>外观主题</span><div class="theme-switch" role="group" aria-label="主题模式"><button :class="{ selected: ui.theme === 'light' }" aria-label="浅色" @click="ui.setTheme('light')">☀</button><button :class="{ selected: ui.theme === 'system' }" aria-label="跟随系统" @click="ui.setTheme('system')">◐</button><button :class="{ selected: ui.theme === 'dark' }" aria-label="深色" @click="ui.setTheme('dark')">☾</button></div></div>
        <button class="drawer-logout" @click="showMore = false; logout()">退出当前账号</button>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api } from './api'
import ConfirmDialog from './components/ConfirmDialog.vue'
import Toast from './components/Toast.vue'
import { MOBILE_NAV, NAV_ITEMS } from './router'
import { useAgentsStore } from './stores/agents'
import { useAuthStore } from './stores/auth'
import { useUiStore } from './stores/ui'
import { formatUptime } from './utils/format'
import { createServerHealth } from './utils/serverHealth'

const auth = useAuthStore()
const agents = useAgentsStore()
const ui = useUiStore()
const router = useRouter()
const route = router.currentRoute
const showMore = ref(false)
// v0.3.3 问题 15：健康状态改由 createServerHealth 管理——轮询失败即标异常、
// 丢弃过期 uptime（旧代码 catch 里“保留上次状态”会把宕机的服务端谎报为正常）；
// 恢复后重拉 Agent，刷新在线计数。
const health = createServerHealth(() => api.dashboard(), {
  onRecovered: () => void agents.load().catch(() => undefined),
})
const { serverStatus, dbStatus, uptimeSeconds, healthOk, healthText } = health
let serverInfoTimer: ReturnType<typeof setInterval> | null = null
const pageTitle = computed(() => (route.value.meta.title as string) || NAV_ITEMS.find((n) => n.name === route.value.name)?.label || 'WakeHub')

function loadServerInfo() {
  return health.loadServerInfo()
}
function stopServerInfoPolling() { if (serverInfoTimer !== null) clearInterval(serverInfoTimer); serverInfoTimer = null; health.reset() }
function startServerInfoPolling() { stopServerInfoPolling(); void loadServerInfo(); serverInfoTimer = setInterval(() => void loadServerInfo(), 60000) }
function onMobileNav(item: { name: string; path: string }) { if (item.name === 'more') { showMore.value = true; return }; void router.push(item.path) }
async function logout() { const ok = await ui.confirm({ title: '退出登录', message: '确认退出当前会话吗？', confirmLabel: '退出登录' }); if (ok) await auth.logout() }
onMounted(() => { ui.initTheme(); auth.bindUnauthorized(() => void router.push({ name: 'login' })); if (auth.me) void agents.load().catch(() => undefined) })
watch(() => auth.me, (me) => { if (me) startServerInfoPolling(); else stopServerInfoPolling() }, { immediate: true })
onUnmounted(stopServerInfoPolling)
</script>
