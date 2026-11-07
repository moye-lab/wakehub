<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-xl font-bold">设备管理</h1>
      <button class="btn-primary min-h-[44px]" @click="showForm = true">+ 新增设备</button>
    </div>

    <!-- 搜索与筛选 -->
    <div class="card flex flex-wrap items-end gap-3 p-4">
      <label class="field min-w-52 flex-1">
        <span>搜索（名称 / IP / MAC）</span>
        <input
          v-model="devices.q"
          class="input"
          placeholder="输入后回车或点查询"
          @keyup.enter="search"
        />
      </label>
      <label class="field">
        <span>状态</span>
        <select v-model="devices.statusFilter" class="input" @change="search">
          <option value="">全部</option>
          <option value="online">在线</option>
          <option value="offline">离线</option>
          <option value="unknown">未知</option>
          <option value="checking">检测中</option>
        </select>
      </label>
      <label class="field">
        <span>Agent</span>
        <select v-model="devices.agentFilter" class="input" @change="search">
          <option value="">全部</option>
          <option v-for="a in agents.items" :key="a.id" :value="a.id">{{ a.name }}</option>
        </select>
      </label>
      <label class="field">
        <span>网络</span>
        <select v-model="devices.networkFilter" class="input" @change="search">
          <option value="">全部</option>
          <option v-for="n in devices.networks" :key="n.id" :value="n.id">{{ formatNetworkLabel(n) }}</option>
        </select>
      </label>
      <button class="btn-secondary min-h-[44px]" @click="search">查询</button>
      <button
        class="btn-primary min-h-[44px]"
        :disabled="selected.size === 0 || batchBusy"
        @click="doBatchWake"
      >
        批量唤醒选中 ({{ selected.size }})
      </button>
    </div>

    <!-- 列表 -->
    <ErrorState v-if="devices.error && devices.items.length > 0" :message="devices.error" compact @retry="retry" />
    <div class="card overflow-hidden">
      <div v-if="devices.loading" class="p-8 text-center text-sm text-slate-500">加载中…</div>
      <ErrorState v-else-if="devices.error && devices.items.length === 0" :message="devices.error" @retry="retry" />
      <EmptyState
        v-else-if="devices.items.length === 0"
        title="暂无设备"
        hint="点击右上角「新增设备」，或先到「局域网扫描」发现设备后导入。"
        icon="🖥️"
      />
      <div v-else class="device-table-wrap overflow-x-auto">
        <table class="w-full min-w-[960px] text-left text-sm">
          <thead class="border-b border-slate-200 bg-slate-50 dark:border-slate-800 dark:bg-slate-800/60">
            <tr>
              <th class="px-3 py-3"><input v-model="selectAll" type="checkbox" class="h-5 w-5 accent-blue-600" aria-label="全选" /></th>
              <th class="px-3 py-3">设备</th>
              <th class="px-3 py-3">MAC / IP</th>
              <th class="px-3 py-3">Agent</th>
              <th class="px-3 py-3">网络</th>
              <th class="px-3 py-3">状态</th>
              <th class="px-3 py-3">最后检测</th>
              <th class="px-3 py-3 text-right">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="d in devices.items" :key="d.id" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
              <td class="px-3 py-3"><input :checked="selected.has(d.id)" type="checkbox" class="h-5 w-5 accent-blue-600" :aria-label="`选中 ${d.name}`" @change="toggleSelect(d.id)" /></td>
              <td class="px-3 py-3">
                <div class="font-medium">{{ d.name }}</div>
                <div class="text-xs text-slate-500 dark:text-slate-400">
                  {{ deviceTypeLabel(d.device_type) }} · {{ d.enabled === false ? '已禁用' : '已启用' }}
                  <span v-if="d.remark">· {{ d.remark }}</span>
                </div>
              </td>
              <td class="px-3 py-3 font-mono text-xs">
                <div class="break-all">{{ d.mac }}</div>
                <div class="text-slate-500 dark:text-slate-400">{{ d.ip || '未配置' }}（端口 {{ d.wol_port }}）</div>
              </td>
              <td class="px-3 py-3 text-xs">
                <span v-if="agentById(d.agent_id)">{{ agentById(d.agent_id)?.name }}</span>
                <span v-else class="text-amber-600 dark:text-amber-400">未分配 Agent</span>
              </td>
              <td class="px-3 py-3 text-xs">
                <div>{{ devices.networkById(d.network_id)?.name || '默认网络' }}</div>
                <div v-if="devices.networkById(d.network_id)?.cidr && devices.networkById(d.network_id)?.cidr !== devices.networkById(d.network_id)?.name" class="font-mono text-slate-500 dark:text-slate-400">{{ devices.networkById(d.network_id)?.cidr }}</div>
              </td>
              <td class="px-3 py-3">
                <StatusBadge :tone="statusTone(d.status)" :label="statusLabel(d.status)" />
                <div v-if="d.last_status_error" class="mt-1 max-w-44 break-words text-xs text-amber-600 dark:text-amber-400">
                  {{ d.last_status_error }}
                </div>
              </td>
              <td class="px-3 py-3 text-xs text-slate-500 dark:text-slate-400">
                {{ formatTimeShort(d.last_checked_at) }}
                <div>{{ d.status_method }}</div>
              </td>
              <td class="px-3 py-3">
                <div class="flex flex-wrap justify-end gap-1.5">
                  <button class="btn-primary min-h-[44px] px-3" :disabled="devices.busyIds.has(d.id) || d.enabled === false" @click="wake(d)">唤醒</button>
                  <button class="btn-secondary min-h-[44px] px-3" :disabled="devices.busyIds.has(d.id) || devices.checkingIds.has(d.id)" @click="check(d)">检测</button>
                  <button
                    v-if="d.sleep_type === 'http_sol' || d.sleep_type === 'ssh'"
                    class="btn-secondary min-h-[44px] px-3"
                    :disabled="devices.busyIds.has(d.id)"
                    @click="sleep(d)"
                  >
                    睡眠
                  </button>
                  <button class="btn-secondary min-h-[44px] px-3" @click="edit(d)">编辑</button>
                  <button class="btn-danger min-h-[44px] px-3" @click="remove(d)">删除</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-if="!devices.loading && devices.items.length > 0" class="device-mobile-list">
        <article v-for="d in devices.items" :key="`mobile-${d.id}`" class="device-mobile-card">
          <div class="device-mobile-head">
            <input :checked="selected.has(d.id)" type="checkbox" class="h-5 w-5 accent-blue-600" :aria-label="`选中 ${d.name}`" @change="toggleSelect(d.id)" />
            <div class="device-mobile-identity"><strong>{{ d.name }}</strong><small>{{ deviceTypeLabel(d.device_type) }} · {{ d.enabled === false ? '已禁用' : '已启用' }}</small></div>
            <StatusBadge :tone="statusTone(d.status)" :label="statusLabel(d.status)" />
          </div>
          <div class="device-mobile-meta"><span>IP <strong>{{ d.ip || '未配置' }}</strong></span><span>MAC <strong>{{ d.mac }}</strong></span></div>
          <div class="device-mobile-meta"><span>Agent <strong>{{ agentById(d.agent_id)?.name || '未分配' }}</strong></span><span>检测 <strong>{{ formatTimeShort(d.last_checked_at) }}</strong></span></div>
          <div class="device-mobile-meta"><span>网络 <strong>{{ devices.networkById(d.network_id)?.name || '默认网络' }}</strong></span><span v-if="devices.networkById(d.network_id)?.cidr && devices.networkById(d.network_id)?.cidr !== devices.networkById(d.network_id)?.name">网段 <strong>{{ devices.networkById(d.network_id)?.cidr }}</strong></span></div>
          <div v-if="d.last_status_error" class="device-mobile-error">{{ d.last_status_error }}</div>
          <div class="device-mobile-actions">
            <button class="btn-primary" :disabled="devices.busyIds.has(d.id) || d.enabled === false" @click="wake(d)">⚡ 唤醒设备</button>
            <details class="device-more-actions"><summary aria-label="更多设备操作">•••</summary><div class="device-more-menu"><button @click="check(d)">检测状态</button><button v-if="d.sleep_type === 'http_sol' || d.sleep_type === 'ssh'" @click="sleep(d)">睡眠</button><button @click="edit(d)">编辑</button><button class="danger-text" @click="remove(d)">删除</button></div></details>
          </div>
        </article>
      </div>
      <div class="border-t border-slate-200 p-4 dark:border-slate-800">
        <Pagination
          :total="devices.total"
          :page="devices.page"
          :page-size="devices.pageSize"
          @change="changePage"
          @size-change="changeSize"
        />
      </div>
    </div>

    <!-- 唤醒回检选项 -->
    <label class="flex items-center gap-2 text-sm text-slate-600 dark:text-slate-300">
      <input v-model="verifyWake" type="checkbox" class="h-5 w-5 accent-blue-600" />
      唤醒后回检设备是否上线（30 秒内轮询检测；魔术包“已发送”不等于“已启动”）
    </label>

    <DeviceForm v-if="showForm" :editing="editingDevice" @close="closeForm" @saved="onSaved" />

    <!-- 批量唤醒结果 -->
    <div v-if="devices.lastBatch" class="fixed inset-0 z-[80] flex items-end justify-center bg-black/60 sm:items-center sm:p-4" @click.self="devices.lastBatch = null">
      <div role="dialog" aria-modal="true" class="card max-h-[92vh] w-full max-w-xl overflow-y-auto p-5">
        <div class="flex items-center justify-between">
          <h2 class="text-lg font-semibold">批量唤醒结果</h2>
          <button class="btn-ghost min-h-[44px] min-w-[44px]" @click="devices.lastBatch = null" aria-label="关闭">✕</button>
        </div>
        <p class="mt-2 text-sm text-slate-600 dark:text-slate-300">
          共 {{ devices.lastBatch.total }} 台 · 已发送 {{ devices.lastBatch.success }} ·
          失败 {{ devices.lastBatch.failed }} · 跳过 {{ devices.lastBatch.skipped }} ·
          耗时 {{ formatDurationMs(devices.lastBatch.duration_ms) }}
        </p>
        <ul class="mt-4 space-y-2 text-sm">
          <li
            v-for="r in devices.lastBatch.results"
            :key="r.device_id"
            class="flex items-start justify-between gap-2 rounded-lg bg-slate-50 px-3 py-2 dark:bg-slate-800"
          >
            <span class="min-w-0">
              <span class="font-medium">{{ r.name }}</span>
              <span class="text-xs text-slate-500"> {{ r.mac }}</span>
              <span v-if="r.reason" class="block text-xs text-slate-500 dark:text-slate-400">{{ r.reason }}</span>
            </span>
            <StatusBadge
              :tone="r.status === 'sent' ? 'green' : r.status === 'failed' ? 'red' : 'gray'"
              :label="r.status === 'sent' ? `已发送${r.boot_status === 'online' ? ' · 已上线' : r.boot_status === 'timeout' ? ' · 未上线' : ''}` : r.status === 'failed' ? '失败' : '跳过'"
            />
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ApiError, DEVICE_TYPES, formatNetworkLabel, type Device } from '../api/types'
import DeviceForm from '../components/DeviceForm.vue'
import EmptyState from '../components/EmptyState.vue'
import ErrorState from '../components/ErrorState.vue'
import Pagination from '../components/Pagination.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { useAgentsStore } from '../stores/agents'
import { useDevicesStore } from '../stores/devices'
import { useUiStore } from '../stores/ui'
import { formatDurationMs, formatTimeShort } from '../utils/format'

const devices = useDevicesStore()
const agents = useAgentsStore()
const ui = useUiStore()

const showForm = ref(false)
const editingDevice = ref<Device | null>(null)
const selected = ref<Set<string>>(new Set())
const verifyWake = ref(false)
const batchBusy = ref(false)

const selectAll = computed({
  get: () => devices.items.length > 0 && devices.items.every((d) => selected.value.has(d.id)),
  set: (v: boolean) => {
    selected.value = v ? new Set(devices.items.map((d) => d.id)) : new Set()
  },
})

function deviceTypeLabel(t: string): string {
  return DEVICE_TYPES.find((x) => x.value === t)?.label ?? '其他'
}

const STATUS_TONE: Record<string, 'green' | 'red' | 'amber' | 'gray'> = {
  online: 'green',
  offline: 'red',
  unknown: 'gray',
  checking: 'amber',
}
const STATUS_LABEL: Record<string, string> = {
  online: '在线',
  offline: '离线',
  unknown: '未知',
  checking: '检测中',
}
function statusTone(s: string) {
  return STATUS_TONE[s] ?? 'gray'
}
function statusLabel(s: string) {
  return STATUS_LABEL[s] ?? s
}
function agentById(id: string) {
  return agents.items.find((a) => a.id === id)
}

function toggleSelect(id: string) {
  if (selected.value.has(id)) selected.value.delete(id)
  else selected.value.add(id)
}

function search() {
  devices.page = 1
  void devices.load()
}

// 加载失败后重试（保留当前筛选与页码）。
function retry() {
  void devices.load()
}

function changePage(p: number) {
  devices.page = p
  void devices.load()
}

function changeSize(size: number) {
  devices.pageSize = size
  devices.page = 1
  void devices.load()
}

function edit(d: Device) {
  editingDevice.value = d
  showForm.value = true
}

// v0.3.3 问题 1 同款取消泄漏：取消也要清空编辑目标，否则下一个新建会覆盖旧设备。
function closeForm() {
  showForm.value = false
  editingDevice.value = null
}

function onSaved() {
  editingDevice.value = null
}

async function wake(d: Device) {
  try {
    const res = await devices.wake(d.id, verifyWake.value)
    if (verifyWake.value) {
      if (res.boot_status === 'online') ui.toast(`${d.name} 魔术包已发送，设备已上线`, 'success')
      else if (res.boot_status === 'timeout') ui.toast(`${d.name} 魔术包已发送，但 30 秒内未检测到设备上线`, 'info')
      else ui.toast(`${d.name} 魔术包已发送`, 'success')
    } else {
      ui.toast(`${d.name} 魔术包已发送（未回检启动状态）`, 'success')
    }
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '唤醒失败', 'error')
  }
}

async function check(d: Device) {
  try {
    const res = await devices.check(d.id)
    ui.toast(`${d.name} 检测结果：${statusLabel(res.state)}${res.error_reason ? `（${res.error_reason}）` : `（${res.latency_ms}ms）`}`, res.state === 'online' ? 'success' : 'info')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '检测失败', 'error')
  }
}

async function sleep(d: Device) {
  const ok = await ui.confirm({
    title: '远程睡眠 / 关机',
    message: `将向 ${d.name}（${d.ip || d.mac}）发送远程关机指令，设备将关机或进入睡眠，确认继续吗？`,
    confirmLabel: '确认发送',
    danger: true,
  })
  if (!ok) return
  try {
    await devices.sleep(d.id, 'shutdown')
    ui.toast(`${d.name} 关机指令已发送`, 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '睡眠指令失败', 'error')
  }
}

async function remove(d: Device) {
  const ok = await ui.confirm({
    title: '删除设备',
    message: `确认删除设备 ${d.name}（${d.mac}）吗？删除后不可恢复。`,
    confirmLabel: '确认删除',
    danger: true,
  })
  if (!ok) return
  try {
    await devices.remove(d.id)
    ui.toast('设备已删除', 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '删除失败', 'error')
  }
}

async function doBatchWake() {
  if (selected.value.size === 0 || batchBusy.value) return
  batchBusy.value = true
  try {
    const res = await devices.batchWake([...selected.value], verifyWake.value)
    ui.toast(`批量唤醒执行完毕：已发送 ${res.success} / 失败 ${res.failed} / 跳过 ${res.skipped}`, 'success')
    selected.value = new Set()
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '批量唤醒失败', 'error')
  } finally {
    batchBusy.value = false
  }
}

onMounted(() => {
  void Promise.all([devices.load(), devices.loadGroups(), devices.loadNetworks(), agents.load()]).then(() => {
    // 刷新即最新（V-N2）：列表加载完自动检测当前页已启用设备，逐行更新状态。
    void devices.refreshAll()
  })
})
</script>
