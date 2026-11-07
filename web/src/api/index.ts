// 类型化端点（路由总表见契约附录 A）。所有列表接口按 §1.4 取分页信封。
import { apiFetch, apiFetchWithWarnings } from './client'
import type {
  Agent,
  BatchWakeResult,
  CheckResult,
  DashboardStats,
  Device,
  Group,
  LogEntry,
  Me,
  Network,
  Page,
  ScanImportResult,
  ScanTask,
  Schedule,
  TaskRun,
  WakeResult,
} from './types'

export const api = {
  // -- 认证（§2.4）--
  login(username: string, password: string) {
    return apiFetch<{ username: string; expires_at: string }>('/api/v1/auth/login', {
      method: 'POST',
      body: { username, password },
    })
  },
  logout() {
    return apiFetch<null>('/api/v1/auth/logout', { method: 'POST' })
  },
  changePassword(current_password: string, new_password: string) {
    return apiFetch<null>('/api/v1/auth/change-password', {
      method: 'POST',
      body: { current_password, new_password },
    })
  },
  me() {
    return apiFetch<Me>('/api/v1/auth/me')
  },

  // -- Dashboard（§10）--
  dashboard() {
    return apiFetch<DashboardStats>('/api/v1/dashboard')
  },

  // -- 设备（§4.3、§5、§6）--
  listDevices(params: {
    q?: string
    status?: string
    agent_id?: string
    network_id?: string
    group_id?: string
    page?: number
    page_size?: number
  }) {
    const qs = new URLSearchParams()
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== '') qs.set(k, String(v))
    }
    const suffix = qs.toString()
    return apiFetch<Page<Device>>(`/api/v1/devices${suffix ? `?${suffix}` : ''}`)
  },
  getDevice(id: string) {
    return apiFetch<Device>(`/api/v1/devices/${id}`)
  },
  createDevice(body: Record<string, unknown>) {
    return apiFetchWithWarnings<Device>('/api/v1/devices', { method: 'POST', body })
  },
  updateDevice(id: string, body: Record<string, unknown>) {
    return apiFetchWithWarnings<Device>(`/api/v1/devices/${id}`, { method: 'PUT', body })
  },
  deleteDevice(id: string) {
    return apiFetch<null>(`/api/v1/devices/${id}`, { method: 'DELETE' })
  },
  wakeDevice(id: string, verify = false, verify_timeout_seconds = 30) {
    return apiFetch<WakeResult>(`/api/v1/devices/${id}/wake`, {
      method: 'POST',
      body: { verify, verify_timeout_seconds },
    })
  },
  batchWake(device_ids: string[], verify = false, verify_timeout_seconds = 30, signal?: AbortSignal) {
    return apiFetch<BatchWakeResult>('/api/v1/devices/batch-wake', {
      method: 'POST',
      body: { device_ids, verify, verify_timeout_seconds },
      signal,
    })
  },
  checkDevice(id: string, auto = false) {
    return apiFetch<CheckResult>(`/api/v1/devices/${id}/check`, {
      method: 'POST',
      // auto=true：页面刷新/周期检测触发的自动检测，后端不写审计；
      // 手动点检测 auto=false，照旧写 device_check 审计（V-N2）。
      body: { auto },
    })
  },
  sleepDevice(id: string, action: 'shutdown' | 'sleep' | 'hibernate') {
    return apiFetch<null>(`/api/v1/devices/${id}/sleep`, { method: 'POST', body: { action } })
  },

  // -- 分组（§11；后端当前返回裸数组，契约要求分页信封——此处做兼容）--
  async listGroups(): Promise<Group[]> {
    const data = await apiFetch<Group[] | Page<Group>>('/api/v1/groups')
    return Array.isArray(data) ? data : data.items
  },
  createGroup(name: string, description = '') {
    return apiFetch<Group>('/api/v1/groups', { method: 'POST', body: { name, description } })
  },

  // -- 网络（v0.2.9 问题 2）--
  async listNetworks(): Promise<Network[]> {
    const data = await apiFetch<Network[] | Page<Network>>('/api/v1/networks')
    return Array.isArray(data) ? data : data.items
  },
  createNetwork(body: { name: string; cidr?: string; description?: string }) {
    return apiFetch<Network>('/api/v1/networks', { method: 'POST', body })
  },
  updateNetwork(id: string, body: { name: string; cidr?: string; description?: string }) {
    return apiFetch<Network>(`/api/v1/networks/${id}`, { method: 'PUT', body })
  },
  deleteNetwork(id: string) {
    return apiFetch<null>(`/api/v1/networks/${id}`, { method: 'DELETE' })
  },
  listAgentNetworks(agentId: string) {
    return apiFetch<{ agent_id: string; networks: Network[] }>(`/api/v1/agents/${agentId}/networks`)
  },
  setAgentNetworks(agentId: string, networkIds: string[]) {
    return apiFetch<{ agent_id: string; networks: Network[] }>(`/api/v1/agents/${agentId}/networks`, {
      method: 'PUT',
      body: { network_ids: networkIds },
    })
  },

  // -- Agent（§3.4）--
  listAgents() {
    return apiFetch<Agent[]>('/api/v1/agents')
  },
  createAgent(body: { agent_id?: string; name: string; subnet?: string }) {
    return apiFetch<{ agent: Agent; token: string }>('/api/v1/agents', {
      method: 'POST',
      body,
    })
  },
  approveAgent(id: string) {
    return apiFetch<{ agent: Agent; token: string; delivered_via: string }>(
      `/api/v1/agents/${id}/approve`,
      { method: 'POST' },
    )
  },
  rejectAgent(id: string) {
    return apiFetch<null>(`/api/v1/agents/${id}/reject`, { method: 'POST' })
  },
  revokeAgentToken(id: string) {
    return apiFetch<{ agent_id: string; revoked: boolean }>(`/api/v1/agents/${id}/revoke-token`, {
      method: 'POST',
    })
  },
  reissueAgentToken(id: string) {
    return apiFetch<{ agent_id: string; token: string }>(`/api/v1/agents/${id}/reissue-token`, {
      method: 'POST',
    })
  },
  disableAgent(id: string) {
    return apiFetch<Agent>(`/api/v1/agents/${id}/disable`, { method: 'POST' })
  },
  enableAgent(id: string) {
    return apiFetch<Agent>(`/api/v1/agents/${id}/enable`, { method: 'POST' })
  },
  deleteAgent(id: string) {
    return apiFetch<null>(`/api/v1/agents/${id}`, { method: 'DELETE' })
  },
  addAgentSubnet(id: string, cidr: string) {
    return apiFetch<string[]>(`/api/v1/agents/${id}/subnets`, { method: 'POST', body: { cidr } })
  },
  removeAgentSubnet(id: string, cidr: string) {
    return apiFetch<string[]>(`/api/v1/agents/${id}/subnets`, { method: 'DELETE', body: { cidr } })
  },

  // -- 扫描（§7.3）--
  createScan(agent_id: string, cidr: string) {
    return apiFetch<{ scan_id: string; agent_id: string; cidr: string; status: string; started_at: string }>(
      '/api/v1/scan',
      { method: 'POST', body: { agent_id, cidr } },
    )
  },
  listScans(page = 1, page_size = 20) {
    return apiFetch<Page<ScanTask>>(`/api/v1/scan?page=${page}&page_size=${page_size}`)
  },
  getScan(scan_id: string) {
    return apiFetch<ScanTask>(`/api/v1/scan/${scan_id}`)
  },
  cancelScan(scan_id: string) {
    return apiFetch<ScanTask>(`/api/v1/scan/${scan_id}`, { method: 'DELETE' })
  },
  importScan(scan_id: string, devices: Array<{ ip: string; mac: string; name?: string }>) {
    return apiFetch<ScanImportResult>('/api/v1/scan/import', {
      method: 'POST',
      body: { scan_id, devices },
    })
  },

  // -- 定时任务（§8.2、§8.3）--
  listTasks(page = 1, page_size = 50) {
    return apiFetch<Page<Schedule>>(`/api/v1/tasks?page=${page}&page_size=${page_size}`)
  },
  createTask(body: Record<string, unknown>) {
    return apiFetch<Schedule>('/api/v1/tasks', { method: 'POST', body })
  },
  updateTask(id: string, body: Record<string, unknown>) {
    return apiFetch<Schedule>(`/api/v1/tasks/${id}`, { method: 'PUT', body })
  },
  deleteTask(id: string) {
    return apiFetch<null>(`/api/v1/tasks/${id}`, { method: 'DELETE' })
  },
  pauseTask(id: string) {
    return apiFetch<Schedule>(`/api/v1/tasks/${id}/pause`, { method: 'POST' })
  },
  resumeTask(id: string) {
    return apiFetch<Schedule>(`/api/v1/tasks/${id}/resume`, { method: 'POST' })
  },
  listTaskRuns(id: string, page = 1, page_size = 50) {
    return apiFetch<Page<TaskRun>>(`/api/v1/tasks/${id}/runs?page=${page}&page_size=${page_size}`)
  },

  // -- 操作日志（§9.1）--
  listLogs(params: {
    from?: string
    to?: string
    action?: string
    target_type?: string
    target_id?: string
    actor?: string
    result?: string
    page?: number
    page_size?: number
  }) {
    const qs = new URLSearchParams()
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== '') qs.set(k, String(v))
    }
    const suffix = qs.toString()
    return apiFetch<Page<LogEntry>>(`/api/v1/logs${suffix ? `?${suffix}` : ''}`)
  },
}
