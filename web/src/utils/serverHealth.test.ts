// 全局健康指示器（v0.3.3 问题 15 收窄：仅修过期指示器）。
import { describe, expect, it, vi } from 'vitest'
import type { DashboardStats } from '../api/types'
import { createServerHealth } from './serverHealth'

function stats(over: Partial<DashboardStats> = {}): DashboardStats {
  return {
    total_devices: 1,
    online_devices: 1,
    offline_devices: 0,
    unknown_devices: 0,
    total_agents: 1,
    online_agents: 1,
    pending_agents: 0,
    server_status: 'ok',
    db_status: 'ok',
    uptime_seconds: 3600,
    version: '0.3.3',
    upcoming_tasks: [],
    ...over,
  }
}

describe('全局健康指示器', () => {
  it('成功轮询后显示服务正常、uptime 保留', async () => {
    const health = createServerHealth(async () => stats())
    await health.loadServerInfo()
    expect(health.healthOk.value).toBe(true)
    expect(health.healthText.value).toBe('服务正常')
    expect(health.uptimeSeconds.value).toBe(3600)
    expect(health.lastHealthyAt.value).not.toBeNull()
  })

  it('一次成功后轮询失败：翻为服务异常、uptime 清空、保留上次正常时间', async () => {
    let calls = 0
    const health = createServerHealth(async () => {
      calls += 1
      if (calls === 1) return stats({ uptime_seconds: 3600 })
      throw new Error('connection refused')
    })
    await health.loadServerInfo()
    expect(health.healthOk.value).toBe(true)
    await health.loadServerInfo()
    // 反向验证：把 catch 改回“保留上次状态”，本例三处断言全部失败。
    expect(health.healthOk.value).toBe(false)
    expect(health.serverStatus.value).toBe('error')
    expect(health.healthText.value).toMatch(/^服务异常/)
    expect(health.uptimeSeconds.value).toBeNull()
    expect(health.lastHealthyAt.value).not.toBeNull()
    expect(health.consecutiveFailures.value).toBe(1)
  })

  it('从未成功过就失败：显示服务异常（无上次正常时间）', async () => {
    const health = createServerHealth(async () => {
      throw new Error('down')
    })
    await health.loadServerInfo()
    expect(health.healthText.value).toBe('服务异常')
    expect(health.uptimeSeconds.value).toBeNull()
    expect(health.lastHealthyAt.value).toBeNull()
  })

  it('恢复后回到正常，并触发 onRecovered（供 App.vue 重拉 Agent）', async () => {
    let calls = 0
    const onRecovered = vi.fn()
    const health = createServerHealth(
      async () => {
        calls += 1
        if (calls === 2) throw new Error('down')
        return stats({ uptime_seconds: 100 + calls })
      },
      { onRecovered },
    )
    await health.loadServerInfo()
    expect(onRecovered).not.toHaveBeenCalled()
    await health.loadServerInfo()
    expect(health.healthOk.value).toBe(false)
    await health.loadServerInfo()
    expect(health.healthOk.value).toBe(true)
    expect(health.healthText.value).toBe('服务正常')
    expect(health.uptimeSeconds.value).toBe(103)
    expect(onRecovered).toHaveBeenCalledTimes(1)
  })

  it('数据库异常时显示数据库异常', async () => {
    const health = createServerHealth(async () => stats({ db_status: 'error' }))
    await health.loadServerInfo()
    expect(health.healthOk.value).toBe(false)
    expect(health.healthText.value).toBe('数据库异常')
  })
})
