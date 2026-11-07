<template>
  <div class="dashboard-page space-y-4">
    <div v-if="loading && !stats" class="py-8 text-center text-sm text-slate-500">加载中…</div>
    <ErrorState v-else-if="error && !stats" :message="error" @retry="refresh" />
    <ErrorState v-else-if="error" :message="error" compact @retry="refresh" />

    <section v-if="stats" class="dashboard-stats" aria-label="设备统计">
      <RouterLink to="/devices" class="dashboard-stat"><span class="stat-dot dot-blue"></span><small>设备总数</small><strong>{{ stats?.total_devices ?? '—' }}</strong></RouterLink>
      <RouterLink to="/devices" class="dashboard-stat"><span class="stat-dot dot-green pulse"></span><small>在线设备</small><strong>{{ stats?.online_devices ?? '—' }}</strong></RouterLink>
      <RouterLink to="/devices" class="dashboard-stat"><span class="stat-dot dot-red"></span><small>离线设备</small><strong>{{ stats?.offline_devices ?? '—' }}</strong></RouterLink>
      <RouterLink to="/devices" class="dashboard-stat"><span class="stat-dot dot-amber"></span><small>未知状态</small><strong>{{ stats?.unknown_devices ?? '—' }}</strong></RouterLink>
      <RouterLink to="/agents" class="dashboard-stat"><span class="stat-dot dot-violet"></span><small>Agent 节点</small><strong>{{ stats?.total_agents ?? '—' }}</strong></RouterLink>
      <RouterLink to="/agents" class="dashboard-stat"><span class="stat-dot dot-green pulse"></span><small>在线 Agent</small><strong>{{ stats?.online_agents ?? '—' }}</strong></RouterLink>
    </section>

    <section class="card dashboard-section p-4">
      <div class="section-heading"><div><h2>快捷唤醒</h2><p>常用设备一键唤醒</p></div><RouterLink to="/devices" class="section-link">管理设备 <span>→</span></RouterLink></div>
      <div v-if="devices.items.length === 0" class="py-5 text-center text-sm text-slate-500 dark:text-slate-400">暂无设备，请前往设备管理添加。</div>
      <div v-else class="quick-device-grid">
        <article v-for="d in devices.items.slice(0, 6)" :key="d.id" class="quick-device-card">
          <div class="quick-device-top"><span class="device-symbol">▣</span><span class="status-chip" :class="`status-${d.status}`"><i></i>{{ deviceStatusLabel(d.status) }}</span></div>
          <strong class="quick-device-name">{{ d.name }}</strong><small class="quick-device-address">{{ d.ip || d.mac }}</small>
          <button class="btn-primary mt-3 min-h-[36px] w-full" :disabled="devices.busyIds.has(d.id) || !d.enabled" @click="wake(d.id)">{{ devices.busyIds.has(d.id) ? '发送中…' : devices.checkingIds.has(d.id) ? '刷新中…' : '⚡ 唤醒' }}</button>
        </article>
      </div>
    </section>

    <div class="dashboard-lower-grid">
      <section class="card dashboard-section p-4">
        <div class="section-heading"><div><h2>即将执行的任务</h2><p>已启用的自动化计划</p></div><RouterLink to="/tasks" class="section-link">全部任务 →</RouterLink></div>
        <div v-if="!stats?.upcoming_tasks?.length" class="empty-inline">暂无已启用的定时任务。</div>
        <ul v-else class="dashboard-list"><li v-for="t in stats.upcoming_tasks" :key="t.schedule_id"><span class="list-leading task-leading">◷</span><span class="list-main"><strong>{{ t.name }}</strong><small>{{ t.target_name ?? '目标已删除' }}</small></span><time>{{ formatTime(t.next_run_at) }}</time></li></ul>
      </section>
      <section class="card dashboard-section p-4">
        <div class="section-heading"><div><h2>最近操作</h2><p>系统最新活动记录</p></div><RouterLink to="/logs" class="section-link">查看日志 →</RouterLink></div>
        <div v-if="logs.items.length === 0" class="empty-inline">暂无操作记录。</div>
        <ul v-else class="dashboard-list"><li v-for="l in logs.items.slice(0, 6)" :key="l.id"><span class="list-leading log-leading">↗</span><span class="list-main"><strong>{{ l.action }}</strong><small>{{ l.detail }}</small></span><time>{{ formatTime(l.created_at) }}</time></li></ul>
      </section>
    </div>
    <div v-if="stats" class="dashboard-version">WakeHub v{{ stats.version }}</div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '../api'
import { ApiError, type DashboardStats } from '../api/types'
import ErrorState from '../components/ErrorState.vue'
import { useAgentsStore } from '../stores/agents'
import { useDevicesStore } from '../stores/devices'
import { useLogsStore } from '../stores/logs'
import { useUiStore } from '../stores/ui'
import { formatTime } from '../utils/format'

const devices = useDevicesStore()
const agents = useAgentsStore()
const logs = useLogsStore()
const ui = useUiStore()

const stats = ref<DashboardStats | null>(null)
const error = ref('')
const loading = ref(false)

const STATUS_LABELS: Record<string, string> = {
  online: '在线',
  offline: '离线',
  unknown: '未知',
  checking: '检测中',
}

function deviceStatusLabel(s: string): string {
  return STATUS_LABELS[s] ?? s
}

async function refresh() {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    const [st] = await Promise.allSettled([
      api.dashboard(),
      devices.load(),
      agents.load(),
      logs.load(),
    ])
    if (st.status === 'fulfilled') stats.value = st.value
    else throw st.reason
    // 刷新即最新（V-N2）：后台自动检测当前页设备，完成后重取统计，
    // 让在线/离线计数与最新状态一致；不阻塞页面渲染。
    void devices.refreshAll().then(async () => {
      try {
        stats.value = await api.dashboard()
      } catch {
        /* 保留旧统计 */
      }
    })
  } catch (e) {
    // allSettled：部分失败不丢全部结果（审计 §六：Promise.all 全有或全无问题）。
    error.value = e instanceof ApiError ? e.message : '加载仪表盘失败'
  } finally {
    loading.value = false
  }
}

async function wake(id: string) {
  try {
    const res = await devices.wake(id, false)
    ui.toast(`魔术包已发送（${res.mac}），启动状态待回检确认`, 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '唤醒失败', 'error')
  }
}

onMounted(() => {
  void refresh()
})
</script>
