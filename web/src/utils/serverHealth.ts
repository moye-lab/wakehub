// 全局健康指示器状态（v0.3.3 问题 15 收窄：仅修过期指示器）。
// 旧行为：健康轮询失败时 catch 里“保留上次状态”，服务端宕机后侧栏/顶栏/页脚
// 无限期谎报“服务正常”。新行为：失败即标为异常并丢弃过期 uptime，恢复时回调
// 让调用方重拉 Agent（App.vue 用它在恢复后刷新在线计数）。
// 抽成纯函数，便于 vitest 用 stub 的 dashboard 覆盖 成功→失败→恢复 全程。
import { computed, ref } from 'vue'
import type { DashboardStats } from '../api/types'

function formatHHmm(d: Date): string {
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

export function createServerHealth(
  fetchDashboard: () => Promise<DashboardStats>,
  opts?: { onRecovered?: () => void },
) {
  const serverStatus = ref<string | null>(null)
  const dbStatus = ref<string | null>(null)
  const uptimeSeconds = ref<number | null>(null)
  /** 上次轮询正常的本地时间（失败后展示“上次正常 HH:mm”用）。 */
  const lastHealthyAt = ref<Date | null>(null)
  const consecutiveFailures = ref(0)

  const healthOk = computed(() => serverStatus.value === 'ok' && dbStatus.value === 'ok')
  const healthText = computed(() => {
    if (serverStatus.value === null) return ''
    if (serverStatus.value !== 'ok') {
      return lastHealthyAt.value ? `服务异常 · 上次正常 ${formatHHmm(lastHealthyAt.value)}` : '服务异常'
    }
    if (dbStatus.value !== 'ok') return '数据库异常'
    return '服务正常'
  })

  async function loadServerInfo() {
    try {
      const st = await fetchDashboard()
      const wasDown = consecutiveFailures.value > 0
      uptimeSeconds.value = st.uptime_seconds
      serverStatus.value = st.server_status
      dbStatus.value = st.db_status
      lastHealthyAt.value = new Date()
      consecutiveFailures.value = 0
      if (wasDown) opts?.onRecovered?.()
    } catch {
      // 失败即过期：不再保留上次正常的 serverStatus / uptime，
      // 否则外壳会把已死的服务端谎报为健康。
      consecutiveFailures.value += 1
      serverStatus.value = 'error'
      uptimeSeconds.value = null
    }
  }

  function reset() {
    serverStatus.value = null
    dbStatus.value = null
    uptimeSeconds.value = null
    lastHealthyAt.value = null
    consecutiveFailures.value = 0
  }

  return {
    serverStatus,
    dbStatus,
    uptimeSeconds,
    lastHealthyAt,
    consecutiveFailures,
    healthOk,
    healthText,
    loadServerInfo,
    reset,
  }
}
