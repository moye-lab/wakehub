<template>
  <div class="fixed inset-0 z-[80] flex items-end justify-center bg-black/60 sm:items-center sm:p-4" @click.self="close">
    <div role="dialog" aria-modal="true" class="card max-h-[92vh] w-full max-w-2xl overflow-y-auto p-5">
      <div class="flex items-center justify-between">
        <h2 class="text-lg font-semibold">{{ editing ? '编辑设备' : '新增设备' }}</h2>
        <button class="btn-ghost min-h-[44px] min-w-[44px]" @click="close" aria-label="关闭">✕</button>
      </div>

      <form class="mt-4 space-y-5" @submit.prevent="submit">
        <!-- 基础信息 -->
        <fieldset>
          <legend class="mb-2 text-sm font-semibold">基础信息</legend>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label class="field">
              <span>设备名称 *</span>
              <input v-model="form.name" class="input" required maxlength="64" placeholder="如 LAN-29" />
              <span v-if="errors.name" class="field-error">{{ errors.name }}</span>
            </label>
            <label class="field">
              <span>MAC 地址 *</span>
              <input
                v-model="macInput"
                class="input font-mono"
                required
                placeholder="AA:BB:CC:DD:EE:FF"
                inputmode="text"
                @input="onMacInput"
              />
              <span v-if="macHint" class="field-hint">{{ macHint }}</span>
              <span v-if="errors.mac" class="field-error">{{ errors.mac }}</span>
            </label>
            <label class="field">
              <span>局域网 IP</span>
              <input v-model="form.ip" class="input font-mono" placeholder="如 192.168.1.29（可空）" inputmode="decimal" />
              <span v-if="errors.ip" class="field-error">{{ errors.ip }}</span>
            </label>
            <label class="field">
              <span>类型</span>
              <select v-model="form.device_type" class="input">
                <option v-for="t in DEVICE_TYPES" :key="t.value" :value="t.value">{{ t.label }}</option>
              </select>
            </label>
            <label class="field">
              <span>所属分组</span>
              <select v-model="form.group_id" class="input">
                <option v-for="g in devices.groups" :key="g.id" :value="g.id">{{ g.name }}</option>
              </select>
              <span v-if="errors.group_id" class="field-error">{{ errors.group_id }}</span>
            </label>
            <label class="field">
              <span>执行 Agent *</span>
              <select v-model="form.agent_id" class="input" required>
                <option value="" disabled>请选择 Agent</option>
                <option v-for="a in approvedAgents" :key="a.id" :value="a.id">
                  {{ a.name }}（{{ a.id }}）{{ a.status === 'connected' ? '' : ' · 离线' }}
                </option>
              </select>
              <span v-if="agentOfflineWarning" class="field-hint text-amber-600 dark:text-amber-400">
                ⚠ {{ agentOfflineWarning }}
              </span>
              <span v-if="agentNetworkHint" class="field-hint">{{ agentNetworkHint }}</span>
              <span v-if="errors.agent_id" class="field-error">{{ errors.agent_id }}</span>
            </label>
            <label class="field">
              <span>所属网络</span>
              <select v-model="form.network_id" class="input">
                <option value="">自动（跟随 Agent）</option>
                <option v-for="n in devices.networks" :key="n.id" :value="n.id">
                  {{ formatNetworkLabel(n) }}
                </option>
              </select>
              <span class="field-hint">MAC 唯一范围为网络内；同一 MAC 可在不同网络各建一台</span>
              <span v-if="networkMismatchWarning" class="field-hint text-amber-600 dark:text-amber-400">
                ⚠ {{ networkMismatchWarning }}
              </span>
              <span v-if="errors.network_id" class="field-error">{{ errors.network_id }}</span>
            </label>
          </div>
          <label class="mt-3 flex items-center gap-2 text-sm">
            <input v-model="form.enabled" type="checkbox" class="h-5 w-5 accent-blue-600" />
            启用该设备（禁用后唤醒/检测/睡眠均跳过）
          </label>
          <label class="field mt-3">
            <span>备注（最多 500 字）</span>
            <input v-model="form.remark" class="input" maxlength="500" placeholder="可选" />
          </label>
        </fieldset>

        <!-- 唤醒配置 -->
        <fieldset>
          <legend class="mb-2 text-sm font-semibold">唤醒配置</legend>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label class="field">
              <span>广播地址</span>
              <input v-model="form.broadcast_address" class="input font-mono" placeholder="255.255.255.255" />
              <span v-if="errors.broadcast_address" class="field-error">{{ errors.broadcast_address }}</span>
            </label>
            <label class="field">
              <span>WOL 端口（1–65535）</span>
              <input v-model.number="form.wol_port" class="input" type="number" min="1" max="65535" />
              <span v-if="errors.wol_port" class="field-error">{{ errors.wol_port }}</span>
            </label>
          </div>
        </fieldset>

        <!-- 状态检测 -->
        <fieldset>
          <legend class="mb-2 text-sm font-semibold">状态检测</legend>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label class="field">
              <span>检测方式</span>
              <select v-model="form.status_method" class="input">
                <option v-for="m in STATUS_METHODS" :key="m.value" :value="m.value">{{ m.label }}</option>
              </select>
            </label>
            <label class="field">
              <span>TCP 检测端口（1–65535）</span>
              <input v-model.number="form.status_port" class="input" type="number" min="1" max="65535" />
              <span class="field-hint">仅 TCP 检测使用；端口关闭不等于设备关机</span>
              <span v-if="errors.status_port" class="field-error">{{ errors.status_port }}</span>
            </label>
          </div>
        </fieldset>

        <!-- 远程关机 -->
        <fieldset>
          <legend class="mb-2 text-sm font-semibold">远程关机 / 睡眠</legend>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label class="field">
              <span>关机适配器</span>
              <select v-model="form.sleep_type" class="input">
                <option value="none">不启用</option>
                <option value="http_sol">HTTP Sleep-on-LAN</option>
                <option value="ssh">SSH 关机（Linux / Windows）</option>
              </select>
            </label>
            <label v-if="form.sleep_type === 'http_sol'" class="field">
              <span>睡眠端点 *</span>
              <input v-model="form.sleep_endpoint" class="input font-mono" placeholder="http://192.168.1.29:8009" />
              <span class="field-hint">仅限局域网地址；http 端点未加密，请优先用 https</span>
              <span v-if="errors.sleep_endpoint" class="field-error">{{ errors.sleep_endpoint }}</span>
            </label>
            <template v-if="form.sleep_type === 'ssh'">
              <label class="field">
                <span>目标系统</span>
                <select v-model="form.ssh_os" class="input">
                  <option value="linux">Linux（NAS / 迷你主机 / 虚拟机 等）</option>
                  <option value="windows">Windows（需开启 OpenSSH Server）</option>
                </select>
              </label>
              <label class="field">
                <span>SSH 用户 *</span>
                <input v-model="form.ssh_user" class="input" placeholder="如 root 或NAS管理员账号" />
                <span v-if="errors.ssh_user" class="field-error">{{ errors.ssh_user }}</span>
              </label>
              <label class="field">
                <span>SSH 密码 {{ editing ? '' : '*' }}</span>
                <input
                  v-model="form.ssh_password"
                  type="password"
                  class="input"
                  :placeholder="editing ? '留空表示保持原密码' : '必填'"
                  autocomplete="new-password"
                />
                <span class="field-hint">
                  密码只存加密后的密文；改密码后需重新信任主机；{{
                    form.ssh_os === 'windows' ? '目标须开启 OpenSSH Server，用 Windows 登录密码' : '目标须为 Linux 且 SSH 已开启'
                  }}
                </span>
                <span v-if="errors.ssh_password" class="field-error">{{ errors.ssh_password }}</span>
              </label>
              <label class="field">
                <span>SSH 端口（1–65535）</span>
                <input v-model.number="form.ssh_port" class="input" type="number" min="1" max="65535" />
                <span v-if="errors.ssh_port" class="field-error">{{ errors.ssh_port }}</span>
              </label>
            </template>
          </div>
        </fieldset>

        <div v-if="generalError" class="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-950 dark:text-rose-300">
          {{ generalError }}
        </div>

        <div class="flex justify-end gap-2">
          <button type="button" class="btn-secondary min-h-[44px]" @click="close">取消</button>
          <button type="submit" class="btn-primary min-h-[44px]" :disabled="submitting">
            {{ submitting ? '保存中…' : editing ? '保存修改' : '新增设备' }}
          </button>
        </div>
      </form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { api } from '../api'
import { ApiError, DEVICE_TYPES, formatNetworkLabel, STATUS_METHODS, type Device, type Network } from '../api/types'
import { useAgentsStore } from '../stores/agents'
import { useDevicesStore } from '../stores/devices'
import { useUiStore } from '../stores/ui'
import { isValidIPv4, isValidMac, isValidPort, normalizeMacInput } from '../utils/validate'

const props = defineProps<{ editing?: Device | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

const agents = useAgentsStore()
const devices = useDevicesStore()
const ui = useUiStore()

const editing = computed(() => !!props.editing)
const form = reactive({
  name: props.editing?.name ?? '',
  mac: props.editing?.mac ?? '',
  ip: props.editing?.ip ?? '',
  device_type: props.editing?.device_type ?? 'pc',
  group_id: props.editing?.group_id ?? 'default',
  agent_id: props.editing?.agent_id ?? '',
  network_id: props.editing?.network_id ?? '',
  enabled: props.editing ? props.editing.enabled !== false : true,
  remark: props.editing?.remark ?? '',
  broadcast_address: props.editing?.broadcast_address ?? '255.255.255.255',
  wol_port: props.editing?.wol_port ?? 9,
  status_method: props.editing?.status_method ?? 'ping',
  status_port: props.editing?.status_port ?? 22,
  sleep_type: props.editing?.sleep_type ?? 'none',
  sleep_endpoint: props.editing?.sleep_endpoint ?? '',
  ssh_user: props.editing?.ssh_user ?? '',
  ssh_port: props.editing?.ssh_port ?? 22,
  ssh_os: props.editing?.ssh_os ?? 'linux',
  ssh_password: '',
})
const macInput = ref(form.mac)
const errors = reactive<Record<string, string>>({})
const generalError = ref('')
const submitting = ref(false)

const approvedAgents = computed(() => agents.items.filter((a) => a.status !== 'pending' && a.status !== 'disabled'))

const macHint = computed(() => {
  if (!macInput.value) return ''
  if (isValidMac(macInput.value)) return ''
  return 'MAC 格式：AA:BB:CC:DD:EE:FF（输入时自动补冒号）'
})

const agentOfflineWarning = computed(() => {
  if (!form.agent_id) return ''
  const a = agents.items.find((x) => x.id === form.agent_id)
  if (a && a.status !== 'connected') return `目标 Agent ${a.name} 当前离线，设备已创建但暂时无法唤醒`
  return ''
})

// 所选 Agent 的管理网络（v0.2.9 问题 15）：拉取失败时只留空、不编造。
const agentNetworks = ref<Network[]>([])

async function loadAgentNetworksForSelected() {
  if (!form.agent_id) {
    agentNetworks.value = []
    return
  }
  try {
    const res = await api.listAgentNetworks(form.agent_id)
    agentNetworks.value = res.networks
  } catch {
    agentNetworks.value = []
  }
}

watch(
  () => form.agent_id,
  () => void loadAgentNetworksForSelected(),
)

onMounted(() => {
  void loadAgentNetworksForSelected()
})

const selectedAgent = computed(() => agents.items.find((x) => x.id === form.agent_id))

// Agent ↔ 网络归属提示：管理网络（名称+CIDR）与上报网段。
const agentNetworkHint = computed(() => {
  const a = selectedAgent.value
  if (!a) return ''
  const parts: string[] = []
  if (agentNetworks.value.length > 0) {
    parts.push('管理网络：' + agentNetworks.value.map((n) => formatNetworkLabel(n)).join('、'))
  }
  if (a.subnet) parts.push(`上报网段 ${a.subnet}`)
  return parts.length ? `该 Agent ${parts.join('，')}` : ''
})

// 显式选了 Agent 不管理的网络时提醒（服务端下发睡眠会按问题 7 拦截）。
const networkMismatchWarning = computed(() => {
  if (!form.network_id || !form.agent_id) return ''
  if (agentNetworks.value.length === 0) return ''
  if (agentNetworks.value.some((n) => n.id === form.network_id)) return ''
  return '该 Agent 不管理所选网络，睡眠可能被范围校验拦截，建议选自动（跟随 Agent）'
})

function onMacInput() {
  const normalized = normalizeMacInput(macInput.value)
  macInput.value = normalized
  form.mac = normalized
}

function validateLocal(): boolean {
  Object.keys(errors).forEach((k) => delete errors[k])
  if (!form.name.trim()) errors.name = '设备名称不能为空'
  if (!isValidMac(form.mac)) errors.mac = 'MAC 地址格式错误，应为 AA:BB:CC:DD:EE:FF'
  if (form.ip && !isValidIPv4(form.ip)) errors.ip = 'IP 地址格式错误'
  if (!form.agent_id) errors.agent_id = '请选择执行 Agent'
  if (!isValidPort(form.wol_port)) errors.wol_port = 'WOL 端口须在 1–65535 之间'
  if (!isValidPort(form.status_port)) errors.status_port = '检测端口须在 1–65535 之间'
  if (form.broadcast_address && !isValidIPv4(form.broadcast_address)) errors.broadcast_address = '广播地址格式错误'
  if (form.sleep_type === 'http_sol' && !form.sleep_endpoint.trim()) errors.sleep_endpoint = '启用 HTTP 关机时须填写睡眠端点'
  if (form.sleep_type === 'ssh') {
    if (!form.ssh_user.trim()) errors.ssh_user = '启用 SSH 关机时须填写 SSH 用户'
    if (!isValidPort(form.ssh_port)) errors.ssh_port = 'SSH 端口须在 1–65535 之间'
    // 编辑时密码留空表示保持原密码；但尚未存过密码时必须填。
    if (!form.ssh_password && (!editing.value || !props.editing?.ssh_password_set)) {
      errors.ssh_password = '启用 SSH 关机时须填写 SSH 密码'
    }
  }
  return Object.keys(errors).length === 0
}

async function submit() {
  if (submitting.value) return
  generalError.value = ''
  if (!validateLocal()) return
  submitting.value = true
  try {
    const body: Record<string, unknown> = { ...form, name: form.name.trim(), mac: form.mac.toUpperCase() }
    let warnings: string[] = []
    if (editing.value && props.editing) {
      ;({ warnings } = await devices.update(props.editing.id, body))
      ui.toast('设备已更新', 'success')
    } else {
      ;({ warnings } = await devices.create(body))
      ui.toast('设备已添加', 'success')
    }
    for (const w of warnings) ui.toast(w, 'info')
    emit('saved')
    emit('close')
  } catch (e) {
    if (e instanceof ApiError) {
      for (const fe of e.errors) errors[fe.field] = fe.message
      if (e.errors.length === 0) generalError.value = e.message
    } else {
      generalError.value = '保存失败，请稍后重试'
    }
  } finally {
    submitting.value = false
  }
}

function close() {
  emit('close')
}
</script>
