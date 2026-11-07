<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-xl font-bold">局域网扫描</h1>
      <button class="btn-secondary min-h-[44px]" @click="loadRecent">查看最近扫描</button>
    </div>

    <!-- 步骤 1：发起扫描 -->
    <div class="card p-4">
      <h2 class="text-sm font-semibold">1 · 选择执行节点与网段</h2>
      <form class="mt-3 flex flex-wrap items-end gap-3" @submit.prevent="startScan">
        <label class="field min-w-52 flex-1">
          <span>执行节点 *</span>
          <select v-model="form.agent_id" class="input" required @change="onAgentChange">
            <option value="" disabled>请选择 Agent</option>
            <option v-for="a in agents.items" :key="a.id" :value="a.id">
              {{ a.name }}（{{ a.status === 'connected' ? '在线' : agentStatusLabel(a.status) }}）
            </option>
          </select>
        </label>
        <label class="field min-w-52 flex-1">
          <span>目标网段 *</span>
          <input v-model="form.cidr" class="input font-mono" placeholder="如 192.168.1.0/24" required />
          <span v-if="cidrHint" class="field-hint">{{ cidrHint }}</span>
        </label>
        <button type="submit" class="btn-primary min-h-[44px]" :disabled="scan.starting || !form.agent_id">
          {{ scan.starting ? '创建中…' : '开始扫描' }}
        </button>
      </form>
      <p class="mt-2 text-xs text-slate-500 dark:text-slate-400">
        只能扫描该 Agent 白名单内的网段（或其子网），掩码不小于 /24；每 Agent 同时只能有 1 个进行中的扫描。
        扫描靠 TCP 轻触刷新 ARP 缓存发现设备（真主动 ARP 需特权，见部署文档）。
      </p>
    </div>

    <!-- 步骤 2：进度 -->
    <div v-if="scan.current" class="card p-4">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h2 class="text-sm font-semibold">
          2 · 扫描进度
          <StatusBadge
            :tone="scan.current.status === 'completed' ? 'green' : scan.current.status === 'running' ? 'blue' : scan.current.status === 'failed' ? 'red' : 'gray'"
            :label="scanStatusLabel(scan.current.status)"
          />
        </h2>
        <button
          v-if="scan.current.status === 'running'"
          class="btn-danger min-h-[44px]"
          @click="cancelScan"
        >
          取消扫描
        </button>
      </div>
      <div class="mt-3">
        <div class="flex justify-between text-xs text-slate-500 dark:text-slate-400">
          <span>已探测 {{ scan.current.progress.scanned }} / {{ scan.current.progress.total }}</span>
          <span>发现 {{ scan.current.progress.found }} · 失败 {{ scan.current.progress.failed }}</span>
        </div>
        <div class="mt-1 h-2 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700" role="progressbar" :aria-valuenow="progressPct" aria-valuemin="0" aria-valuemax="100">
          <div class="h-full bg-blue-600 transition-all" :style="{ width: `${progressPct}%` }" />
        </div>
        <p v-if="scan.current.error" class="mt-2 text-sm text-rose-600 dark:text-rose-400">{{ scan.current.error }}</p>
        <p class="mt-2 text-xs text-slate-500 dark:text-slate-400">
          {{ scan.current.agent_id }} · {{ scan.current.cidr }} · 开始 {{ formatTime(scan.current.started_at) }}
          <span v-if="scan.current.finished_at">· 完成 {{ formatTime(scan.current.finished_at) }} · 耗时 {{ formatDurationMs(scan.current.duration_ms) }}</span>
        </p>
      </div>

      <!-- 步骤 3：结果与导入 -->
      <div v-if="scan.current.status === 'completed' || scan.current.status === 'cancelled'" class="mt-5">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h3 class="text-sm font-semibold">3 · 扫描结果（{{ scan.current.results.length }} 台）</h3>
          <div class="flex gap-2">
            <button class="btn-secondary min-h-[44px]" @click="scan.selectAll(importable)">全选未导入</button>
            <button class="btn-secondary min-h-[44px]" @click="scan.clearSelection">清空选择</button>
            <button class="btn-primary min-h-[44px]" :disabled="scan.selected.size === 0 || scan.importing" @click="doImport">
              {{ scan.importing ? '导入中…' : `导入选中 (${scan.selected.size})` }}
            </button>
          </div>
        </div>
        <div v-if="scan.current.results.length === 0" class="py-4 text-xs text-slate-500 dark:text-slate-400">
          未发现设备。
        </div>
        <div v-else class="mt-3 overflow-x-auto">
          <table class="w-full min-w-[640px] text-left text-sm">
            <thead class="border-b border-slate-200 dark:border-slate-800">
              <tr>
                <th class="py-2 pr-2"><span class="sr-only">选择</span></th>
                <th class="py-2 pr-2">IP</th>
                <th class="py-2 pr-2">MAC</th>
                <th class="py-2 pr-2">厂商</th>
                <th class="py-2 pr-2">状态</th>
                <th class="py-2">发现时间</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="d in scan.current.results" :key="d.mac" class="border-b border-slate-100 last:border-0 dark:border-slate-800">
                <td class="py-2 pr-2">
                  <input
                    :checked="scan.selected.has(d.mac)"
                    :disabled="d.exists"
                    type="checkbox"
                    class="h-5 w-5 accent-blue-600"
                    :aria-label="`选择 ${d.ip}`"
                    @change="scan.toggleSelect(d)"
                  />
                </td>
                <td class="py-2 pr-2 font-mono text-xs">{{ d.ip }}</td>
                <td class="py-2 pr-2 font-mono text-xs">{{ d.mac }}</td>
                <td class="py-2 pr-2 text-xs">{{ d.vendor }}</td>
                <td class="py-2 pr-2">
                  <StatusBadge v-if="d.exists" tone="gray" label="已存在" />
                  <StatusBadge v-else tone="green" label="新发现" />
                </td>
                <td class="py-2 text-xs text-slate-500 dark:text-slate-400">{{ formatTime(d.first_seen_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="scan.lastImport" class="mt-3 rounded-lg bg-emerald-50 px-3 py-2 text-sm text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300">
          导入完成：新增 {{ scan.lastImport.imported }} 台，跳过 {{ scan.lastImport.skipped }} 台（已存在）。
          <ul class="mt-1 list-disc pl-5 text-xs">
            <li v-for="r in scan.lastImport.results" :key="r.mac">
              {{ r.ip }} · {{ r.mac }} — {{ r.status === 'created' ? '已创建' : `跳过（${r.reason}）` }}
            </li>
          </ul>
        </div>
      </div>
    </div>

    <!-- 最近扫描 -->
    <div class="card p-4">
      <h2 class="text-sm font-semibold">最近扫描任务</h2>
      <ErrorState v-if="scan.error" :message="scan.error" @retry="loadRecent" />
      <div v-else-if="scan.recent.length === 0" class="py-4 text-xs text-slate-500 dark:text-slate-400">暂无扫描记录。</div>
      <ul v-else class="mt-3 space-y-2 text-sm">
        <li
          v-for="t in scan.recent"
          :key="t.scan_id"
          class="flex flex-wrap items-center justify-between gap-2 rounded-lg bg-slate-50 px-3 py-2 dark:bg-slate-800"
        >
          <span class="min-w-0">
            <span class="font-mono text-xs">{{ t.cidr }}</span>
            <span class="text-xs text-slate-500 dark:text-slate-400"> · {{ t.agent_id }} · 发现 {{ t.progress.found }} 台</span>
          </span>
          <span class="flex items-center gap-2">
            <StatusBadge
              :tone="t.status === 'completed' ? 'green' : t.status === 'running' ? 'blue' : t.status === 'failed' ? 'red' : 'gray'"
              :label="scanStatusLabel(t.status)"
            />
            <button class="btn-secondary min-h-[44px] px-3" @click="openScan(t.scan_id)">查看</button>
          </span>
        </li>
      </ul>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive } from 'vue'
import { ApiError } from '../api/types'
import ErrorState from '../components/ErrorState.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { useAgentsStore } from '../stores/agents'
import { useScanStore } from '../stores/scan'
import { useUiStore } from '../stores/ui'
import { formatDurationMs, formatTime } from '../utils/format'
import { cidrWithinAllowed, isValidScanCIDR } from '../utils/validate'

const agents = useAgentsStore()
const scan = useScanStore()
const ui = useUiStore()

const form = reactive({ agent_id: '', cidr: '' })

const progressPct = computed(() => {
  const p = scan.current?.progress
  if (!p || p.total === 0) return 0
  return Math.round((p.scanned / p.total) * 100)
})

const importable = computed(() => (scan.current?.results ?? []).filter((d) => !d.exists))

const cidrHint = computed(() => {
  if (!form.cidr) return ''
  if (!isValidScanCIDR(form.cidr)) return '网段须为私有 CIDR 且掩码不小于 /24，如 192.168.1.0/24'
  if (form.agent_id) {
    const a = agents.items.find((x) => x.id === form.agent_id)
    if (a && !cidrWithinAllowed(form.cidr, a.allowed_subnets ?? [])) {
      return `该网段不在 ${a.name} 的白名单内（白名单：${(a.allowed_subnets ?? []).join('、') || '无'}）`
    }
  }
  return ''
})

const SCAN_STATUS_LABEL: Record<string, string> = {
  running: '进行中',
  completed: '已完成',
  failed: '失败',
  cancelled: '已取消',
}
function scanStatusLabel(s: string): string {
  return SCAN_STATUS_LABEL[s] ?? s
}
const AGENT_STATUS_LABEL: Record<string, string> = {
  pending: '待审批',
  approved: '已批准（离线）',
  connected: '在线',
  disabled: '已禁用',
}
function agentStatusLabel(s: string): string {
  return AGENT_STATUS_LABEL[s] ?? s
}

function onAgentChange() {
  const a = agents.items.find((x) => x.id === form.agent_id)
  if (a?.subnet && (!form.cidr || form.cidr === '192.168.1.0/24')) {
    form.cidr = a.subnet
  }
}

async function startScan() {
  if (!isValidScanCIDR(form.cidr)) {
    ui.toast('网段须为私有 CIDR 且掩码不小于 /24', 'error')
    return
  }
  try {
    await scan.start(form.agent_id, form.cidr)
    ui.toast('扫描任务已创建，正在轮询进度', 'info')
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      ui.toast('该 Agent 已有进行中的扫描', 'error')
    } else {
      ui.toast(e instanceof ApiError ? e.message : '扫描创建失败', 'error')
    }
  }
}

async function cancelScan() {
  const ok = await ui.confirm({
    title: '取消扫描',
    message: '取消进行中的扫描？已发现的结果仍可导入。',
    confirmLabel: '确认取消',
    danger: true,
  })
  if (!ok) return
  try {
    await scan.cancel()
    ui.toast('扫描已取消', 'info')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '取消失败', 'error')
  }
}

async function doImport() {
  try {
    const res = await scan.importSelected()
    if (!res) return
    ui.toast(`导入完成：新增 ${res.imported} 台，跳过 ${res.skipped} 台`, 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '导入失败', 'error')
  }
}

async function openScan(scanId: string) {
  try {
    await scan.open(scanId)
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '打开扫描失败', 'error')
  }
}

async function loadRecent() {
  // 失败文案进 scan.error（ErrorState 展示），此处不再弹 toast。
  await scan.loadRecent()
}

onMounted(() => {
  void Promise.all([
    agents.load(),
    scan.loadRecent(),
  ]).then(() => {
    const first = agents.items.find((a) => a.status === 'connected') ?? agents.items[0]
    if (first) {
      form.agent_id = first.id
      if (first.subnet) form.cidr = first.subnet
    }
  })
})

onUnmounted(() => {
  scan.stopPolling()
})
</script>
