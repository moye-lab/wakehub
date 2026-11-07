// 设备 store：列表（分页+搜索+筛选）、创建/更新/删除、
// 唤醒（sent ≠ booted，契约 §5.1）、批量唤醒、检测、睡眠。
import { defineStore } from 'pinia'
import { reactive, ref } from 'vue'
import { api } from '../api'
import { ApiError, type BatchWakeResult, type CheckResult, type Device, type Group, type Network, type WakeResult } from '../api/types'

export const useDevicesStore = defineStore('devices', () => {
  const items = ref<Device[]>([])
  const total = ref(0)
  const page = ref(1)
  const pageSize = ref(50)
  const loading = ref(false)
  // 列表加载失败文案（v0.2.9 问题 16）：load() 不再向外抛错，失败时由
  // 视图按 error 渲染 ErrorState（带重试），不再误显示“暂无设备”。
  const error = ref('')
  const q = ref('')
  const statusFilter = ref('')
  const agentFilter = ref('')
  const networkFilter = ref('')
  const groups = ref<Group[]>([])
  const networks = ref<Network[]>([])
  /** 选择器用全量设备（v0.3.3 问题 5）：与分页列表 items 分离，DevicesView 的
   *  当前页/筛选不受影响。TaskForm 的目标下拉用它，不再只读第一页。 */
  const allItems = ref<Device[]>([])
  const allLoading = ref(false)
  let loadAllPromise: Promise<void> | null = null
  /** 批量唤醒最近一次结果（供结果弹窗渲染 per-device 明细）。 */
  const lastBatch = ref<BatchWakeResult | null>(null)
  /** 正在执行动作（唤醒/睡眠）的设备：按钮显示“发送中…”。 */
  const busyIds = reactive(new Set<string>())
  /** 正在刷新状态（手动/自动检测）的设备：按钮显示“刷新中…”，
   *  与 busyIds 分开——检测只是读状态，绝不能写成“发送中”误导用户。 */
  const checkingIds = reactive(new Set<string>())

  async function load() {
    loading.value = true
    error.value = ''
    try {
      const data = await api.listDevices({
        q: q.value || undefined,
        status: statusFilter.value || undefined,
        agent_id: agentFilter.value || undefined,
        network_id: networkFilter.value || undefined,
        page: page.value,
        page_size: pageSize.value,
      })
      items.value = data.items
      total.value = data.total
      page.value = data.page
    } catch (e) {
      error.value = e instanceof ApiError ? e.message : '加载设备失败'
    } finally {
      loading.value = false
    }
  }

  /** 分页拉取全部设备进 allItems（v0.3.3 问题 5）。不带任何筛选，不碰
   *  items/page/pageSize；并发调用合并为一次；失败只记警告、保留旧数据。 */
  async function loadAll(): Promise<void> {
    if (loadAllPromise) return loadAllPromise
    loadAllPromise = (async () => {
      allLoading.value = true
      try {
        const pageSizeAll = 50
        const collected: Device[] = []
        let pageNo = 1
        let totalCount = Number.POSITIVE_INFINITY
        while (collected.length < totalCount && pageNo <= 200) {
          const data = await api.listDevices({ page: pageNo, page_size: pageSizeAll })
          collected.push(...data.items)
          totalCount = data.total
          if (data.items.length < pageSizeAll) break
          pageNo += 1
        }
        allItems.value = collected
      } catch (e) {
        console.warn('加载全部设备失败：', e instanceof ApiError ? e.message : e)
      } finally {
        allLoading.value = false
      }
    })()
    try {
      await loadAllPromise
    } finally {
      loadAllPromise = null
    }
  }

  /** loadAll 的别名：选择器入口语义更直白。 */
  const loadForSelector = loadAll

  // 辅助下拉（分组/网络）失败不拦主列表：保留旧数据、只记控制台警告。
  async function loadGroups() {
    try {
      groups.value = await api.listGroups()
    } catch (e) {
      console.warn('加载分组失败：', e instanceof ApiError ? e.message : e)
    }
  }

  async function loadNetworks() {
    try {
      networks.value = await api.listNetworks()
    } catch (e) {
      console.warn('加载网络失败：', e instanceof ApiError ? e.message : e)
    }
  }

  function networkById(id: string) {
    return networks.value.find((n) => n.id === id)
  }

  async function create(body: Record<string, unknown>): Promise<{ warnings: string[] }> {
    const { data, warnings } = await api.createDevice(body)
    items.value.unshift(data)
    total.value += 1
    return { warnings: warnings.map((w) => w.message) }
  }

  async function update(id: string, body: Record<string, unknown>): Promise<{ warnings: string[] }> {
    const { data, warnings } = await api.updateDevice(id, body)
    const idx = items.value.findIndex((d) => d.id === id)
    if (idx >= 0) items.value[idx] = data
    return { warnings: warnings.map((w) => w.message) }
  }

  async function remove(id: string) {
    await api.deleteDevice(id)
    items.value = items.value.filter((d) => d.id !== id)
    total.value = Math.max(0, total.value - 1)
  }

  async function wake(id: string, verify: boolean): Promise<WakeResult> {
    busyIds.add(id)
    try {
      return await api.wakeDevice(id, verify)
    } finally {
      busyIds.delete(id)
    }
  }

  async function batchWake(ids: string[], verify: boolean): Promise<BatchWakeResult> {
    const res = await api.batchWake(ids, verify)
    lastBatch.value = res
    return res
  }

  async function check(id: string, auto = false): Promise<CheckResult> {
    checkingIds.add(id)
    try {
      const res = await api.checkDevice(id, auto)
      const idx = items.value.findIndex((d) => d.id === id)
      if (idx >= 0 && res.state !== 'checking') {
        items.value[idx] = {
          ...items.value[idx],
          status: res.state,
          last_checked_at: res.checked_at,
          last_status_error: res.error_reason,
        }
      }
      return res
    } finally {
      checkingIds.delete(id)
    }
  }

  /** 页面刷新即最新（V-N2）：自动检测当前页全部已启用设备，逐行更新状态。 */
  const refreshingAll = ref(false)
  async function refreshAll(): Promise<number> {
    if (refreshingAll.value) return 0
    refreshingAll.value = true
    try {
      // enabled 为 null 按启用算（后端默认启用语义）。
      const queue = items.value.filter((d) => d.enabled !== false)
      let n = 0
      // 并发上限 5，避免一次刷新打爆 Agent/服务端。
      const workers = Array.from({ length: Math.min(5, queue.length) }, async () => {
        while (queue.length > 0) {
          const d = queue.shift()!
          n += 1
          // 单台失败不阻断其余，保留旧状态；自动检测不写审计、不弹提示。
          try {
            await check(d.id, true)
          } catch {
            /* ignore */
          }
        }
      })
      await Promise.all(workers)
      return n
    } finally {
      refreshingAll.value = false
    }
  }

  async function sleep(id: string, action: 'shutdown' | 'sleep' | 'hibernate') {
    busyIds.add(id)
    try {
      await api.sleepDevice(id, action)
    } finally {
      busyIds.delete(id)
    }
  }

  /** 表单校验错误按字段取（契约 §1.2 结构化错误）。 */
  function fieldError(err: unknown, field: string): string | undefined {
    if (err instanceof ApiError) return err.fieldError(field)
    return undefined
  }

  return {
    items, total, page, pageSize, loading, error, q, statusFilter, agentFilter, networkFilter,
    groups, networks, allItems, allLoading, lastBatch, busyIds, checkingIds, refreshingAll,
    load, loadAll, loadForSelector, loadGroups, loadNetworks, networkById, create, update, remove, wake, batchWake, check, refreshAll, sleep, fieldError,
  }
})
