import { beforeEach, describe, expect, it, vi } from 'vitest'
import { apiFetch, setUnauthorizedHandler } from './client'
import { ApiError } from './types'

function mockResponse(status: number, body: string, headers: Record<string, string> = {}) {
  return new Response(body, {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  })
}

describe('apiFetch', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    setUnauthorizedHandler(null)
  })

  it('成功时剥掉信封返回 data', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      mockResponse(200, JSON.stringify({ code: 0, message: 'ok', data: { total: 3 } })),
    ))
    const data = await apiFetch<{ total: number }>('/api/v1/devices')
    expect(data).toEqual({ total: 3 })
    const call = (fetch as unknown as { mock: { calls: unknown[][] } }).mock.calls[0]
    expect((call[1] as RequestInit).credentials).toBe('same-origin')
    expect((call[1] as RequestInit).headers).toBeUndefined()
  })

  it('失败时带回字段级错误（§1.2）', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      mockResponse(400, JSON.stringify({
        code: 400,
        message: '设备信息校验失败',
        data: { errors: [{ field: 'mac', code: 'invalid_format', message: 'MAC 地址格式错误' }] },
      })),
    ))
    const err = await apiFetch('/api/v1/devices', { method: 'POST', body: {} }).catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(400)
    expect((err as ApiError).fieldError('mac')).toBe('MAC 地址格式错误')
  })

  it('401 触发未登录回调（不拼 Authorization 头）', async () => {
    const on401 = vi.fn()
    setUnauthorizedHandler(on401)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      mockResponse(401, JSON.stringify({ code: 401, message: '未登录或登录已过期', data: null })),
    ))
    const err = await apiFetch('/api/v1/dashboard').catch((e) => e)
    expect((err as ApiError).status).toBe(401)
    expect(on401).toHaveBeenCalledTimes(1)
  })

  it('非 JSON 错误体不抛 SyntaxError（F-N4）', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      new Response('<html>Bad Gateway</html>', { status: 502, headers: { 'Content-Type': 'text/html' } }),
    ))
    const err = await apiFetch('/api/v1/devices').catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(502)
    expect((err as ApiError).message).toContain('Bad Gateway')
  })

  it('429 限流带 Retry-After 提示', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      mockResponse(429, JSON.stringify({ code: 429, message: '请求过于频繁', data: null }), { 'Retry-After': '60' }),
    ))
    const err = await apiFetch('/api/v1/devices').catch((e) => e)
    expect((err as ApiError).status).toBe(429)
    expect((err as ApiError).message).toContain('60 秒')
  })

  it('网络不可达时给可读文案', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    const err = await apiFetch('/api/v1/devices').catch((e) => e)
    expect((err as ApiError).message).toContain('网络请求失败')
  })
})
