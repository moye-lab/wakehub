// 扫描 store：异步任务（202 + scan_id）→ 1s 轮询进度 → 取消 →
// 导入（逐条 best-effort，契约 §7.3）。进行中不返回部分结果，
// 轮询到 404 即视为任务已失效（服务端重启后内存任务丢失）。
import { defineStore } from 'pinia'
import { reactive, ref } from 'vue'
import { api } from '../api'
import { ApiError, type DiscoveredDevice, type ScanImportResult, type ScanTask } from '../api/types'

export const useScanStore = defineStore('scan', () => {
  const current = ref<ScanTask | null>(null)
  const recent = ref<ScanTask[]>([])
  // 最近扫描加载失败文案（v0.2.9 问题 16），见 devices.ts。
  const error = ref('')
  const starting = ref(false)
  const importing = ref(false)
  const selected = reactive(new Set<string>())
  const lastImport = ref<ScanImportResult | null>(null)
  let pollTimer: number | null = null

  function stopPolling() {
    if (pollTimer !== null) {
      window.clearInterval(pollTimer)
      pollTimer = null
    }
  }

  async function pollOnce() {
    if (!current.value) return
    try {
      const task = await api.getScan(current.value.scan_id)
      current.value = task
      if (task.status !== 'running') stopPolling()
    } catch (e) {
      // 404 = 任务已失效（服务端重启/过期淘汰），停止轮询并提示。
      if (e instanceof ApiError && e.status === 404) {
        stopPolling()
        current.value = null
      }
    }
  }

  function startPolling() {
    stopPolling()
    pollTimer = window.setInterval(() => void pollOnce(), 1000)
  }

  async function start(agent_id: string, cidr: string) {
    starting.value = true
    try {
      const created = await api.createScan(agent_id, cidr)
      current.value = await api.getScan(created.scan_id)
      selected.clear()
      lastImport.value = null
      startPolling()
      return current.value
    } finally {
      starting.value = false
    }
  }

  async function cancel() {
    if (!current.value || current.value.status !== 'running') return
    current.value = await api.cancelScan(current.value.scan_id)
    stopPolling()
  }

  async function loadRecent() {
    error.value = ''
    try {
      const data = await api.listScans(1, 20)
      recent.value = data.items
    } catch (e) {
      error.value = e instanceof ApiError ? e.message : '加载最近扫描失败'
    }
  }

  async function open(scan_id: string) {
    current.value = await api.getScan(scan_id)
    selected.clear()
    lastImport.value = null
    if (current.value.status === 'running') startPolling()
    else stopPolling()
  }

  function toggleSelect(d: DiscoveredDevice) {
    const key = d.mac
    if (selected.has(key)) selected.delete(key)
    else selected.add(key)
  }

  function selectAll(importable: DiscoveredDevice[]) {
    for (const d of importable) selected.add(d.mac)
  }

  function clearSelection() {
    selected.clear()
  }

  async function importSelected() {
    if (!current.value) return null
    const devices = current.value.results
      .filter((d) => selected.has(d.mac))
      .map((d) => ({ ip: d.ip, mac: d.mac }))
    if (devices.length === 0) return null
    importing.value = true
    try {
      const res = await api.importScan(current.value.scan_id, devices)
      lastImport.value = res
      selected.clear()
      return res
    } finally {
      importing.value = false
    }
  }

  return {
    current, recent, error, starting, importing, selected, lastImport,
    start, cancel, loadRecent, open, pollOnce, stopPolling,
    toggleSelect, selectAll, clearSelection, importSelected,
  }
})
