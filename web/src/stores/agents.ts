// Agent store：列表、预签发（路径 A）、审批/驳回（路径 B）、
// 吊销/重新签发（轮换）、禁用/启用、删除、白名单管理（契约 §3.4）。
import { defineStore } from 'pinia'
import { reactive, ref } from 'vue'
import { api } from '../api'
import { ApiError, type Agent } from '../api/types'

export const useAgentsStore = defineStore('agents', () => {
  const items = ref<Agent[]>([])
  const loading = ref(false)
  // 列表加载失败文案（v0.2.9 问题 16），见 devices.ts。
  const error = ref('')
  const busyIds = reactive(new Set<string>())
  /** 刚签发的 token（仅显示一次，服务端永不再返回）。 */
  const lastIssuedToken = ref<{ agentId: string; token: string } | null>(null)

  const pendingCount = () => items.value.filter((a) => a.status === 'pending').length
  const onlineCount = () => items.value.filter((a) => a.status === 'connected').length

  async function load() {
    loading.value = true
    error.value = ''
    try {
      items.value = await api.listAgents()
    } catch (e) {
      error.value = e instanceof ApiError ? e.message : '加载 Agent 失败'
    } finally {
      loading.value = false
    }
  }

  function patch(id: string, agent: Agent) {
    const idx = items.value.findIndex((a) => a.id === id)
    if (idx >= 0) items.value[idx] = agent
  }

  async function create(body: { agent_id?: string; name: string; subnet?: string }) {
    const { agent, token } = await api.createAgent(body)
    items.value.push(agent)
    lastIssuedToken.value = { agentId: agent.id, token }
    return agent
  }

  async function approve(id: string) {
    busyIds.add(id)
    try {
      const { agent, token } = await api.approveAgent(id)
      patch(id, agent)
      if (token) lastIssuedToken.value = { agentId: id, token }
      return agent
    } finally {
      busyIds.delete(id)
    }
  }

  async function reject(id: string) {
    busyIds.add(id)
    try {
      await api.rejectAgent(id)
      items.value = items.value.filter((a) => a.id !== id)
    } finally {
      busyIds.delete(id)
    }
  }

  async function revokeToken(id: string) {
    busyIds.add(id)
    try {
      await api.revokeAgentToken(id)
      await load()
    } finally {
      busyIds.delete(id)
    }
  }

  async function reissueToken(id: string) {
    busyIds.add(id)
    try {
      const { token } = await api.reissueAgentToken(id)
      lastIssuedToken.value = { agentId: id, token }
      await load()
    } finally {
      busyIds.delete(id)
    }
  }

  async function disable(id: string) {
    busyIds.add(id)
    try {
      patch(id, await api.disableAgent(id))
    } finally {
      busyIds.delete(id)
    }
  }

  async function enable(id: string) {
    busyIds.add(id)
    try {
      patch(id, await api.enableAgent(id))
    } finally {
      busyIds.delete(id)
    }
  }

  async function remove(id: string) {
    busyIds.add(id)
    try {
      await api.deleteAgent(id)
      items.value = items.value.filter((a) => a.id !== id)
    } finally {
      busyIds.delete(id)
    }
  }

  async function addSubnet(id: string, cidr: string): Promise<string[]> {
    const subnets = await api.addAgentSubnet(id, cidr)
    const idx = items.value.findIndex((a) => a.id === id)
    if (idx >= 0) items.value[idx] = { ...items.value[idx], allowed_subnets: subnets }
    return subnets
  }

  async function removeSubnet(id: string, cidr: string): Promise<string[]> {
    const subnets = await api.removeAgentSubnet(id, cidr)
    const idx = items.value.findIndex((a) => a.id === id)
    if (idx >= 0) items.value[idx] = { ...items.value[idx], allowed_subnets: subnets }
    return subnets
  }

  return {
    items, loading, error, busyIds, lastIssuedToken,
    pendingCount, onlineCount,
    load, create, approve, reject, revokeToken, reissueToken,
    disable, enable, remove, addSubnet, removeSubnet,
  }
})
