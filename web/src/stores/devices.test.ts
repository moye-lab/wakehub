// 设备 store 的 refreshAll（V-N2：页面刷新即最新）测试。
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { api } from '../api'
import { useDevicesStore } from './devices'
import type { Device } from '../api/types'

vi.mock('../api', () => ({
  api: {
    listDevices: vi.fn(),
    checkDevice: vi.fn(),
    listNetworks: vi.fn(),
  },
}))

const mockedApi = api as unknown as {
  listDevices: ReturnType<typeof vi.fn>
  checkDevice: ReturnType<typeof vi.fn>
  listNetworks: ReturnType<typeof vi.fn>
}

function device(over: Partial<Device> & { id: string }): Device {
  return {
    name: 'LAN-' + over.id,
    mac: 'AA:BB:CC:00:00:01',
    ip: '192.168.1.3',
    agent_id: 'a1',
    network_id: 'default',
    broadcast_address: '255.255.255.255',
    wol_port: 9,
    device_type: 'computer',
    status_method: 'ping',
    status_port: 9,
    status: 'unknown',
    last_checked_at: null,
    last_status_error: '',
    group_id: '',
    enabled: true,
    sleep_type: '',
    sleep_endpoint: '',
    ssh_user: '',
    ssh_port: 22,
    ssh_password_set: false,
    ssh_host_key_sha256: '',
    ssh_os: 'linux',
    ssh_use_sudo: false,
    remark: '',
    created_at: '',
    updated_at: '',
    ...over,
  }
}

describe('refreshAll', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('只检测已启用设备（enabled=null 按启用算），逐行更新状态', async () => {
    const store = useDevicesStore()
    store.items = [
      device({ id: 'on', enabled: true }),
      device({ id: 'nil', enabled: null }),
      device({ id: 'off', enabled: false }),
    ]
    mockedApi.checkDevice.mockImplementation(async (id: string) => ({
      state: 'online',
      method: 'ping',
      checked_at: '2026-11-06T08:40:00Z',
      latency_ms: 1,
      error_reason: '',
    }))
    const n = await store.refreshAll()
    expect(n).toBe(2)
    expect(mockedApi.checkDevice).toHaveBeenCalledTimes(2)
    // 自动检测带 auto=true，后端不写审计。
    expect(mockedApi.checkDevice).toHaveBeenCalledWith('on', true)
    expect(mockedApi.checkDevice).toHaveBeenCalledWith('nil', true)
    expect(store.items.find((d) => d.id === 'on')?.status).toBe('online')
    expect(store.items.find((d) => d.id === 'nil')?.status).toBe('online')
    expect(store.items.find((d) => d.id === 'off')?.status).toBe('unknown')
    expect(store.refreshingAll).toBe(false)
  })

  it('单台失败不阻断其余，保留旧状态', async () => {
    const store = useDevicesStore()
    store.items = [device({ id: 'bad' }), device({ id: 'good' })]
    mockedApi.checkDevice.mockImplementation(async (id: string) => {
      if (id === 'bad') throw new Error('目标节点离线')
      return { state: 'offline', method: 'ping', checked_at: '2026-11-06T08:40:00Z', latency_ms: 0, error_reason: '' }
    })
    const n = await store.refreshAll()
    expect(n).toBe(2)
    expect(store.items.find((d) => d.id === 'good')?.status).toBe('offline')
    expect(store.items.find((d) => d.id === 'bad')?.status).toBe('unknown')
  })

  it('并发调用直接返回 0，不重复检测', async () => {
    const store = useDevicesStore()
    store.items = [device({ id: 'a' }), device({ id: 'b' })]
    mockedApi.checkDevice.mockImplementation(
      async () => new Promise((resolve) => {
        setTimeout(() => resolve({ state: 'online', method: 'ping', checked_at: '', latency_ms: 1, error_reason: '' }), 30)
      }),
    )
    const [n1, n2] = await Promise.all([store.refreshAll(), store.refreshAll()])
    expect([n1, n2].sort()).toEqual([0, 2])
    expect(mockedApi.checkDevice).toHaveBeenCalledTimes(2)
  })

  it('并发上限 5', async () => {
    const store = useDevicesStore()
    store.items = Array.from({ length: 12 }, (_, i) => device({ id: 'd' + i }))
    let inFlight = 0
    let maxFlight = 0
    mockedApi.checkDevice.mockImplementation(async () => {
      inFlight += 1
      maxFlight = Math.max(maxFlight, inFlight)
      await new Promise((r) => setTimeout(r, 20))
      inFlight -= 1
      return { state: 'online', method: 'ping', checked_at: '', latency_ms: 1, error_reason: '' }
    })
    const n = await store.refreshAll()
    expect(n).toBe(12)
    expect(maxFlight).toBeLessThanOrEqual(5)
    expect(maxFlight).toBeGreaterThan(1)
  })

  it('check 默认走手动模式（auto=false）', async () => {
    const store = useDevicesStore()
    store.items = [device({ id: 'x' })]
    mockedApi.checkDevice.mockResolvedValue({ state: 'online', method: 'ping', checked_at: '', latency_ms: 1, error_reason: '' })
    await store.check('x')
    expect(mockedApi.checkDevice).toHaveBeenCalledWith('x', false)
  })

  it('check 走 checkingIds，不占用 busyIds（刷新时按钮显示“刷新中”而非“发送中”）', async () => {
    const store = useDevicesStore()
    store.items = [device({ id: 'x' })]
    let release!: (v: unknown) => void
    mockedApi.checkDevice.mockImplementation(() => new Promise((resolve) => { release = resolve }))
    const p = store.check('x')
    expect(store.checkingIds.has('x')).toBe(true)
    expect(store.busyIds.has('x')).toBe(false)
    release({ state: 'online', method: 'ping', checked_at: '', latency_ms: 1, error_reason: '' })
    await p
    expect(store.checkingIds.has('x')).toBe(false)
    expect(store.busyIds.has('x')).toBe(false)
    expect(store.items[0].status).toBe('online')
  })
})

describe('networks', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('loadNetworks 拉取网络列表，networkById 按 id 取名', async () => {
    const store = useDevicesStore()
    mockedApi.listNetworks.mockResolvedValue([
      { id: 'default', name: '默认网络', cidr: '', description: '', created_at: '', updated_at: '' },
      { id: 'net-1', name: '实验室', cidr: '192.168.2.0/24', description: '', created_at: '', updated_at: '' },
    ])
    await store.loadNetworks()
    expect(store.networks).toHaveLength(2)
    expect(store.networkById('net-1')?.name).toBe('实验室')
    expect(store.networkById('no-such')).toBeUndefined()
  })

  it('load 在 networkFilter 有值时传 network_id', async () => {
    const store = useDevicesStore()
    mockedApi.listDevices.mockResolvedValue({ items: [], total: 0, page: 1 })
    store.networkFilter = 'net-1'
    await store.load()
    expect(mockedApi.listDevices).toHaveBeenCalledWith(expect.objectContaining({ network_id: 'net-1' }))
    store.networkFilter = ''
    await store.load()
    expect(mockedApi.listDevices).toHaveBeenCalledWith(expect.objectContaining({ network_id: undefined }))
  })
})

describe('load error state（v0.2.9 问题 16）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('listDevices 失败时 error 被设置、旧列表保留、load() 不抛错', async () => {
    const store = useDevicesStore()
    mockedApi.listDevices.mockResolvedValue({ items: [device({ id: 'd1' })], total: 1, page: 1 })
    await store.load()
    expect(store.items).toHaveLength(1)
    expect(store.error).toBe('')

    mockedApi.listDevices.mockRejectedValue(new Error('boom'))
    await expect(store.load()).resolves.toBeUndefined()
    expect(store.error).toBe('加载设备失败')
    expect(store.items).toHaveLength(1)
    expect(store.loading).toBe(false)
  })

  it('随后成功加载后 error 清空', async () => {
    const store = useDevicesStore()
    mockedApi.listDevices.mockRejectedValue(new Error('boom'))
    await store.load()
    expect(store.error).not.toBe('')
    mockedApi.listDevices.mockResolvedValue({ items: [], total: 0, page: 1 })
    await store.load()
    expect(store.error).toBe('')
    expect(store.items).toHaveLength(0)
  })
})
