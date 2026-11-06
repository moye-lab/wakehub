// WakeHub v0.2.0 API 数据模型（契约 docs/contract-v0.2.0.md）。
// 字段名与后端 Go 结构体的 json tag 逐一对应；后端若改字段，此处须同步改。

export interface Envelope<T> {
  code: number
  message: string
  data: T
  warnings?: Array<{ code: string; message: string }>
}

export interface FieldError {
  field: string
  code: string
  message: string
}

export interface Page<T> {
  items: T[]
  total: number
  page: number
  page_size: number
}

export class ApiError extends Error {
  status: number
  code: number | string
  errors: FieldError[]
  constructor(message: string, status: number, code: number | string = status, errors: FieldError[] = []) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.errors = errors
  }
  fieldError(field: string): string | undefined {
    return this.errors.find((e) => e.field === field)?.message
  }
}

export interface Device {
  id: string
  name: string
  mac: string
  ip: string
  agent_id: string
  network_id: string
  broadcast_address: string
  wol_port: number
  device_type: string
  status_method: string
  status_port: number
  status: string
  last_checked_at: string | null
  last_status_error: string
  group_id: string
  enabled: boolean | null
  sleep_type: string
  sleep_endpoint: string
  ssh_user: string
  ssh_port: number
  ssh_password_set: boolean
  ssh_host_key_sha256: string
  ssh_os: string
  // v0.2.12：“使用 sudo 关机”（Linux 专用）。勾选后 Agent 用已存 SSH 密码
  // 提权执行关机，目标机无需预先写 polkit 规则；非 SSH / 非 Linux 时无效。
  ssh_use_sudo: boolean
  remark: string
  created_at: string
  updated_at: string
}

export type DeviceType = 'pc' | 'server' | 'nas' | 'raspberry_pi' | 'tv_box' | 'router' | 'other'
export type StatusMethod = 'ping' | 'tcp' | 'arp'
export type SleepType = 'none' | 'http_sol' | 'ssh'

/** 网络实体（v0.2.9 问题 2）：MAC 唯一范围为“网络内唯一”。 */
export interface Network {
  id: string
  name: string
  cidr: string
  description: string
  created_at: string
  updated_at: string
}

// 网络显示名：名字与网段相同时只显示名字，避免 "网络 192.168.1.0/24（192.168.1.0/24）"
// 之类的重复（v0.2.10：009 迁移把种子网络改名为 CIDR 本身后，名字即网段）。
export function formatNetworkLabel(n: Network): string {
  return n.cidr && n.cidr !== n.name ? `${n.name}（${n.cidr}）` : n.name
}

// “使用 sudo 关机”开关是否适用（v0.2.12）：只对 SSH 适配器的 Linux 目标
// 有意义；Windows 目标（命令本就同步返回、失败可见）与非 SSH 适配器不适用，
// 后端与仓库层同样会归零（DeviceForm 提交时也先归一化一次）。
export function sshSudoApplies(sleepType: string, sshOs: string): boolean {
  return sleepType === 'ssh' && sshOs === 'linux'
}

export const DEVICE_TYPES: Array<{ value: DeviceType; label: string }> = [
  { value: 'pc', label: '电脑' },
  { value: 'server', label: '服务器' },
  { value: 'nas', label: 'NAS' },
  { value: 'raspberry_pi', label: '树莓派' },
  { value: 'tv_box', label: '电视盒子' },
  { value: 'router', label: '路由器' },
  { value: 'other', label: '其他' },
]

export const STATUS_METHODS: Array<{ value: StatusMethod; label: string }> = [
  { value: 'ping', label: 'Ping 检测' },
  { value: 'tcp', label: 'TCP 端口检测' },
  { value: 'arp', label: 'ARP 表检测' },
]

export interface Group {
  id: string
  name: string
  description?: string
  created_at?: string
}

export interface Agent {
  id: string
  name: string
  subnet: string
  allowed_subnets: string[]
  status: 'pending' | 'approved' | 'connected' | 'disabled'
  authorized: boolean
  ip: string
  os: string
  arch: string
  version: string
  last_seen_at: string | null
  last_heartbeat_at: string | null
  created_at: string
}

export interface CheckResult {
  state: 'online' | 'offline' | 'unknown' | 'checking'
  method: string
  checked_at: string
  latency_ms: number
  error_reason: string
}

export interface WakeResult {
  device_id: string
  mac: string
  send_status: string
  sent_at: string
  boot_status: 'pending' | 'online' | 'timeout' | 'unknown' | null
  boot_checked_at: string | null
}

export interface BatchWakeDeviceResult {
  device_id: string
  name: string
  mac: string
  status: 'sent' | 'failed' | 'skipped'
  reason: string
  sent_at: string | null
  boot_status: 'pending' | 'online' | 'timeout' | 'unknown' | null
  boot_checked_at: string | null
}

export interface BatchWakeResult {
  batch_id: string
  total: number
  success: number
  failed: number
  skipped: number
  results: BatchWakeDeviceResult[]
  started_at: string
  finished_at: string
  duration_ms: number
}

export interface DiscoveredDevice {
  ip: string
  mac: string
  vendor: string
  first_seen_at: string
  exists: boolean
  existing_device_id: string | null
  agent_id: string
  subnet: string
}

export interface ScanProgress {
  scanned: number
  total: number
  found: number
  failed: number
}

export interface ScanTask {
  scan_id: string
  agent_id: string
  cidr: string
  status: 'running' | 'completed' | 'failed' | 'cancelled'
  progress: ScanProgress
  results: DiscoveredDevice[]
  started_at: string
  finished_at: string | null
  duration_ms: number
  error: string
}

export interface ScanImportItem {
  ip: string
  mac: string
  device_id: string | null
  status: 'created' | 'skipped'
  reason: string
}

export interface ScanImportResult {
  scan_id: string
  agent_id: string
  imported: number
  skipped: number
  results: ScanImportItem[]
}

export interface Schedule {
  id: string
  name: string
  cron_expr: string
  timezone: string
  target_type: 'device' | 'group'
  target_id: string
  target_name: string | null
  action: string
  retry_count: number
  run_once: boolean
  enabled: boolean
  last_run_at: string | null
  last_run_status: 'success' | 'failed' | 'skipped' | 'partial' | null
  next_run_at: string | null
  created_at: string
}

export interface RunDeviceResult {
  device_id: string
  name: string
  status: 'success' | 'failed' | 'skipped'
  reason: string
}

export interface TaskRun {
  id: string
  schedule_id: string
  status: 'success' | 'partial' | 'failed' | 'skipped'
  output: string
  devices: RunDeviceResult[]
  started_at: string
  finished_at: string
  duration_ms: number
}

export interface LogEntry {
  id: string
  actor: string
  action: string
  result: 'success' | 'failed'
  target_type: string
  target_id: string
  detail: string
  ip_address: string
  created_at: string
}

export interface UpcomingTask {
  schedule_id: string
  name: string
  target_name: string | null
  next_run_at: string
}

export interface DashboardStats {
  total_devices: number
  online_devices: number
  offline_devices: number
  unknown_devices: number
  total_agents: number
  online_agents: number
  pending_agents: number
  server_status: string
  db_status: string
  uptime_seconds: number
  version: string
  upcoming_tasks: UpcomingTask[]
}

export interface Me {
  username: string
  role: string
  issued_at: string
  expires_at: string
}
