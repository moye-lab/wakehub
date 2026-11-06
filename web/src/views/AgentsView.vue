<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-xl font-bold">Agent 节点</h1>
      <button class="btn-primary min-h-[44px]" @click="showCreate = true">+ 预签发 Agent</button>
    </div>

    <p class="text-sm text-slate-500 dark:text-slate-400">
      每个 Agent 持独立 token 连接服务端（契约 §3.1）。预签发：管理员创建后把一次性 token 写进 Agent；
      自助注册：Agent 持入网 token 连接后进入待审批，在此审批。
      <span v-if="agents.pendingCount() > 0" class="font-medium text-amber-600 dark:text-amber-400">
        当前有 {{ agents.pendingCount() }} 个待审批。
      </span>
    </p>

    <ErrorState v-if="agents.error && agents.items.length > 0" :message="agents.error" compact @retry="retry" />
    <div v-if="agents.loading" class="py-8 text-center text-sm text-slate-500">加载中…</div>
    <ErrorState v-else-if="agents.error" :message="agents.error" @retry="retry" />
    <EmptyState
      v-else-if="agents.items.length === 0"
      title="暂无 Agent"
      hint="点击右上角「预签发 Agent」创建，或让 Agent 持入网 token 自助注册后在此审批。"
      icon="📡"
    />
    <div v-else class="grid gap-4 md:grid-cols-2">
      <div v-for="a in agents.items" :key="a.id" class="card space-y-3 p-4">
        <div class="flex items-start justify-between gap-2">
          <div class="min-w-0">
            <div class="truncate font-semibold">{{ a.name }}</div>
            <div class="truncate font-mono text-xs text-slate-500 dark:text-slate-400">{{ a.id }}</div>
          </div>
          <StatusBadge
            :tone="a.status === 'connected' ? 'green' : a.status === 'pending' ? 'amber' : a.status === 'disabled' ? 'red' : 'gray'"
            :label="agentStatusLabel(a.status)"
          />
        </div>
        <dl class="grid grid-cols-2 gap-x-3 gap-y-1 text-xs">
          <dt class="text-slate-500 dark:text-slate-400">IP</dt><dd class="font-mono break-all">{{ a.ip || '—' }}</dd>
          <dt class="text-slate-500 dark:text-slate-400">系统</dt><dd>{{ a.os || '—' }} / {{ a.arch || '—' }}</dd>
          <dt class="text-slate-500 dark:text-slate-400">版本</dt><dd>{{ a.version || '—' }}</dd>
          <dt class="text-slate-500 dark:text-slate-400">上报网段</dt><dd class="font-mono break-all">{{ a.subnet || '—' }}</dd>
          <dt class="text-slate-500 dark:text-slate-400">最后心跳</dt><dd>{{ formatTime(a.last_heartbeat_at) }}</dd>
          <dt class="text-slate-500 dark:text-slate-400">最后上线</dt><dd>{{ formatTime(a.last_seen_at) }}</dd>
        </dl>
        <div>
          <div class="mb-1 text-xs font-medium text-slate-500 dark:text-slate-400">扫描白名单</div>
          <div class="flex flex-wrap gap-1.5">
            <span v-for="s in a.allowed_subnets" :key="s" class="inline-flex items-center gap-1 rounded-full bg-slate-100 px-2 py-0.5 font-mono text-xs dark:bg-slate-800">
              {{ s }}
              <button class="text-rose-500" :aria-label="`移除白名单 ${s}`" @click="removeSubnet(a, s)">✕</button>
            </span>
            <span v-if="!a.allowed_subnets?.length" class="text-xs text-slate-400">未配置</span>
          </div>
          <form class="mt-2 flex gap-2" @submit.prevent="addSubnet(a)">
            <input v-model="subnetInputs[a.id]" class="input min-h-[44px] flex-1 font-mono" placeholder="新增网段，如 192.168.1.0/24" />
            <button type="submit" class="btn-secondary min-h-[44px]">添加</button>
          </form>
        </div>
        <div>
          <div class="mb-1 text-xs font-medium text-slate-500 dark:text-slate-400">管理网络</div>
          <div class="flex flex-wrap gap-1.5">
            <span v-for="n in agentNetworks[a.id] ?? []" :key="n.id" class="inline-flex items-center gap-1 rounded-full bg-blue-50 px-2 py-0.5 text-xs text-blue-700 dark:bg-blue-950 dark:text-blue-300">
              {{ formatNetworkLabel(n) }}
            </span>
            <span v-if="!(agentNetworks[a.id] ?? []).length" class="text-xs text-slate-400">未分配</span>
          </div>
        </div>
        <div class="flex flex-wrap gap-1.5 border-t border-slate-100 pt-3 dark:border-slate-800">
          <button v-if="a.status === 'pending'" class="btn-primary min-h-[44px]" :disabled="agents.busyIds.has(a.id)" @click="approve(a)">审批通过</button>
          <button v-if="a.status === 'pending'" class="btn-danger min-h-[44px]" :disabled="agents.busyIds.has(a.id)" @click="reject(a)">驳回</button>
          <button v-if="a.status !== 'pending'" class="btn-secondary min-h-[44px]" :disabled="agents.busyIds.has(a.id)" @click="reissue(a)">重新签发 Token</button>
          <button v-if="a.status !== 'pending'" class="btn-secondary min-h-[44px]" :disabled="agents.busyIds.has(a.id)" @click="revoke(a)">吊销 Token</button>
          <button v-if="a.status === 'disabled'" class="btn-primary min-h-[44px]" :disabled="agents.busyIds.has(a.id)" @click="enable(a)">启用</button>
          <button v-if="a.status !== 'disabled' && a.status !== 'pending'" class="btn-secondary min-h-[44px]" :disabled="agents.busyIds.has(a.id)" @click="disable(a)">禁用</button>
          <button class="btn-danger min-h-[44px]" :disabled="agents.busyIds.has(a.id)" @click="removeAgent(a)">删除</button>
        </div>
      </div>
    </div>

    <!-- 预签发表单 -->
    <div v-if="showCreate" class="fixed inset-0 z-[80] flex items-end justify-center bg-black/60 sm:items-center sm:p-4" @click.self="showCreate = false">
      <div role="dialog" aria-modal="true" class="card w-full max-w-md p-5">
        <div class="flex items-center justify-between">
          <h2 class="text-lg font-semibold">预签发 Agent</h2>
          <button class="btn-ghost min-h-[44px] min-w-[44px]" @click="showCreate = false" aria-label="关闭">✕</button>
        </div>
        <form class="mt-4 space-y-4" @submit.prevent="create">
          <label class="field">
            <span>Agent ID（可选，不填自动生成）</span>
            <input v-model="createForm.agent_id" class="input font-mono" placeholder="如 agent-home-lan" />
            <span class="field-hint">字母数字、下划线、连字符，首字符须为字母或数字；不得为 local</span>
          </label>
          <label class="field">
            <span>名称 *</span>
            <input v-model="createForm.name" class="input" required maxlength="64" placeholder="如 Home-HostNetwork-Agent" />
          </label>
          <label class="field">
            <span>所在网段（可选）</span>
            <input v-model="createForm.subnet" class="input font-mono" placeholder="如 192.168.1.0/24" />
            <span class="field-hint">注册时自动写入扫描白名单</span>
          </label>
          <p v-if="createError" class="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-950 dark:text-rose-300">{{ createError }}</p>
          <div class="flex justify-end gap-2">
            <button type="button" class="btn-secondary min-h-[44px]" @click="showCreate = false">取消</button>
            <button type="submit" class="btn-primary min-h-[44px]" :disabled="creating">创建并签发 Token</button>
          </div>
        </form>
      </div>
    </div>

    <AgentTokenModal
      v-if="agents.lastIssuedToken"
      :token="agents.lastIssuedToken.token"
      @close="agents.lastIssuedToken = null"
    />
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { api } from '../api'
import { ApiError, formatNetworkLabel, type Agent, type Network } from '../api/types'
import AgentTokenModal from '../components/AgentTokenModal.vue'
import EmptyState from '../components/EmptyState.vue'
import ErrorState from '../components/ErrorState.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { useAgentsStore } from '../stores/agents'
import { useUiStore } from '../stores/ui'
import { formatTime } from '../utils/format'
import { isValidCIDR } from '../utils/validate'

const agents = useAgentsStore()
const ui = useUiStore()

const showCreate = ref(false)
const creating = ref(false)
const createError = ref('')
const createForm = reactive({ agent_id: '', name: '', subnet: '' })
const subnetInputs = reactive<Record<string, string>>({})
/** 各 Agent 管理的网络（v0.2.9 问题 2），只读展示，分配走 API。 */
const agentNetworks = reactive<Record<string, Network[]>>({})

async function loadAgentNetworks() {
  for (const a of agents.items) {
    try {
      const res = await api.listAgentNetworks(a.id)
      agentNetworks[a.id] = res.networks
    } catch {
      agentNetworks[a.id] = []
    }
  }
}

const STATUS_LABEL: Record<string, string> = {
  pending: '待审批',
  approved: '已批准（离线）',
  connected: '在线',
  disabled: '已禁用',
}
function agentStatusLabel(s: string): string {
  return STATUS_LABEL[s] ?? s
}

async function create() {
  createError.value = ''
  if (!createForm.name.trim()) {
    createError.value = '名称不能为空'
    return
  }
  if (createForm.agent_id && !/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(createForm.agent_id)) {
    createError.value = 'Agent ID 格式错误'
    return
  }
  if (createForm.subnet && !isValidCIDR(createForm.subnet)) {
    createError.value = '网段须为合法 CIDR，如 192.168.1.0/24'
    return
  }
  creating.value = true
  try {
    await agents.create({
      agent_id: createForm.agent_id || undefined,
      name: createForm.name.trim(),
      subnet: createForm.subnet || undefined,
    })
    showCreate.value = false
    createForm.agent_id = ''
    createForm.name = ''
    createForm.subnet = ''
    ui.toast('Agent 已创建，Token 仅显示一次，请立即保存', 'success')
  } catch (e) {
    createError.value = e instanceof ApiError ? e.message : '创建失败'
  } finally {
    creating.value = false
  }
}

async function approve(a: Agent) {
  try {
    await agents.approve(a.id)
    ui.toast(`${a.name} 已审批通过`, 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '审批失败', 'error')
  }
}

async function reject(a: Agent) {
  const ok = await ui.confirm({
    title: '驳回注册',
    message: `驳回 ${a.name} 的注册申请？该 Agent 行将被删除。`,
    confirmLabel: '确认驳回',
    danger: true,
  })
  if (!ok) return
  try {
    await agents.reject(a.id)
    ui.toast('已驳回并删除该 Agent', 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '驳回失败', 'error')
  }
}

async function revoke(a: Agent) {
  const ok = await ui.confirm({
    title: '吊销 Token',
    message: `吊销 ${a.name} 的当前 Token？在线会话将立即断开，须重新签发才能再连。`,
    confirmLabel: '确认吊销',
    danger: true,
  })
  if (!ok) return
  try {
    await agents.revokeToken(a.id)
    ui.toast('Token 已吊销', 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '吊销失败', 'error')
  }
}

async function reissue(a: Agent) {
  const ok = await ui.confirm({
    title: '重新签发 Token（轮换）',
    message: `重新签发 ${a.name} 的 Token？旧 Token 立即失效，无宽限期。`,
    confirmLabel: '确认签发',
    danger: true,
  })
  if (!ok) return
  try {
    await agents.reissueToken(a.id)
    ui.toast('新 Token 已签发，请立即保存', 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '签发失败', 'error')
  }
}

async function disable(a: Agent) {
  const ok = await ui.confirm({
    title: '禁用 Agent',
    message: `禁用 ${a.name}？在线会话将立即断开，其设备的唤醒等操作将返回失败。`,
    confirmLabel: '确认禁用',
    danger: true,
  })
  if (!ok) return
  try {
    await agents.disable(a.id)
    ui.toast('Agent 已禁用', 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '禁用失败', 'error')
  }
}

async function enable(a: Agent) {
  try {
    await agents.enable(a.id)
    ui.toast('Agent 已启用', 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '启用失败', 'error')
  }
}

async function removeAgent(a: Agent) {
  const ok = await ui.confirm({
    title: '删除 Agent',
    message: `删除 ${a.name}？其设备不删除，但后续操作会因 Agent 不存在而失败，须重新分配。`,
    confirmLabel: '确认删除',
    danger: true,
  })
  if (!ok) return
  try {
    await agents.remove(a.id)
    ui.toast('Agent 已删除', 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '删除失败', 'error')
  }
}

async function addSubnet(a: Agent) {
  const cidr = (subnetInputs[a.id] ?? '').trim()
  if (!isValidCIDR(cidr)) {
    ui.toast('网段须为合法 CIDR，如 192.168.1.0/24', 'error')
    return
  }
  try {
    await agents.addSubnet(a.id, cidr)
    subnetInputs[a.id] = ''
    ui.toast('白名单已更新', 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '添加失败', 'error')
  }
}

async function removeSubnet(a: Agent, cidr: string) {
  const ok = await ui.confirm({
    title: '移除白名单网段',
    message: `从 ${a.name} 的扫描白名单移除 ${cidr}？`,
    confirmLabel: '确认移除',
    danger: true,
  })
  if (!ok) return
  try {
    await agents.removeSubnet(a.id, cidr)
    ui.toast('白名单已更新', 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '移除失败', 'error')
  }
}

onMounted(() => {
  void agents.load().then(() => void loadAgentNetworks())
})

// 加载失败后重试（保留列表，管理网络一并重拉）。
function retry() {
  void agents.load().then(() => void loadAgentNetworks())
}
</script>
