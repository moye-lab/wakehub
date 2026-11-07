// 操作日志 store：筛选（事件/用户/目标/结果/时间范围）+ 分页（契约 §9.1）。
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '../api'
import { ApiError, type LogEntry } from '../api/types'

export interface LogFilters {
  action: string
  actor: string
  target_type: string
  target_id: string
  result: string
  from: string
  to: string
}

export const KNOWN_ACTIONS = [
  'login_success', 'login_failed', 'auth_password_change', 'auth_logout',
  'device_create', 'device_update', 'device_delete', 'device_wake', 'device_wake_failed',
  'device_batch_wake', 'device_check', 'device_check_failed', 'device_sleep', 'device_sleep_failed',
  'group_create', 'network_scan', 'scan_import', 'scan_cancel',
  'agent_register', 'agent_approve', 'agent_reject', 'agent_revoke', 'agent_reissue',
  'agent_disable', 'agent_enable', 'agent_delete', 'agent_update', 'agent_connect', 'agent_disconnect',
  'schedule_create', 'schedule_update', 'schedule_delete', 'schedule_pause', 'schedule_resume',
  'schedule_run', 'audit_cleanup',
]

export const useLogsStore = defineStore('logs', () => {
  const items = ref<LogEntry[]>([])
  const total = ref(0)
  const page = ref(1)
  const pageSize = ref(50)
  const loading = ref(false)
  // 列表加载失败文案（v0.2.9 问题 16），见 devices.ts。
  const error = ref('')
  const filters = ref<LogFilters>({
    action: '', actor: '', target_type: '', target_id: '', result: '', from: '', to: '',
  })

  async function load() {
    loading.value = true
    error.value = ''
    try {
      const data = await api.listLogs({
        action: filters.value.action || undefined,
        actor: filters.value.actor || undefined,
        target_type: filters.value.target_type || undefined,
        target_id: filters.value.target_id || undefined,
        result: filters.value.result || undefined,
        from: filters.value.from || undefined,
        to: filters.value.to || undefined,
        page: page.value,
        page_size: pageSize.value,
      })
      items.value = data.items
      total.value = data.total
      page.value = data.page
    } catch (e) {
      error.value = e instanceof ApiError ? e.message : '加载操作日志失败'
    } finally {
      loading.value = false
    }
  }

  function resetFilters() {
    filters.value = { action: '', actor: '', target_type: '', target_id: '', result: '', from: '', to: '' }
    page.value = 1
  }

  return { items, total, page, pageSize, loading, error, filters, load, resetFilters }
})
