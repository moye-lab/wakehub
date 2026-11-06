// 统一 API 客户端（契约 §1、§2.2）。
// - 同源 SPA：fetch 带 credentials: 'same-origin'，凭 HttpOnly Cookie 会话。
//   任何地方不得再拼 Authorization 头、不得把 token 写进 localStorage（F-N1）。
// - 响应信封 {code, message, data, warnings?}；code 等于 HTTP 状态码。
// - 错误处理（F-N4）：非 JSON 错误体不再抛 SyntaxError，
//   而是按状态码给可读文案；结构化字段错误（§1.2）原样带回。
import { ApiError, type Envelope, type FieldError } from './types'

export type UnauthorizedHandler = () => void

let onUnauthorized: UnauthorizedHandler | null = null

export function setUnauthorizedHandler(fn: UnauthorizedHandler | null): void {
  onUnauthorized = fn
}

export interface RequestOptions {
  method?: string
  body?: unknown
  signal?: AbortSignal
}

/** 把各种失败响应统一成 ApiError；成功返回 data（信封外壳已剥掉）。 */
export async function apiFetch<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const init: RequestInit = {
    method: options.method ?? 'GET',
    credentials: 'same-origin',
    signal: options.signal,
  }
  if (options.body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(options.body)
  }

  let resp: Response
  try {
    resp = await fetch(path, init)
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e
    throw new ApiError('网络请求失败，请检查服务端是否可达', 0, 'network_error')
  }

  const text = await resp.text()
  let json: Envelope<unknown> | null = null
  if (text) {
    try {
      json = JSON.parse(text) as Envelope<unknown>
    } catch {
      // 后端返回了非 JSON（如网关 502 页面）：不要丢信息，
      // 把 HTTP 状态与原始片段放进错误文案（F-N4）。
      if (!resp.ok) {
        throw new ApiError(`请求失败（HTTP ${resp.status}）：${text.slice(0, 120)}`, resp.status)
      }
      throw new ApiError('服务端返回了无法解析的响应', resp.status)
    }
  }

  if (resp.status === 401) {
    onUnauthorized?.()
    throw new ApiError(json?.message || '未登录或登录已过期', 401, 401)
  }
  if (!resp.ok || (json && json.code !== 0)) {
    const status = resp.status
    const code = json?.code ?? status
    const message = json?.message || `请求失败（HTTP ${status}）`
    let errors: FieldError[] = []
    if (json && json.data && typeof json.data === 'object') {
      const d = json.data as { errors?: FieldError[] }
      if (Array.isArray(d.errors)) errors = d.errors
    }
    if (status === 429) {
      const retryAfter = resp.headers.get('Retry-After')
      throw new ApiError(
        retryAfter ? `${message}，请 ${retryAfter} 秒后再试` : message,
        status,
        code,
        errors,
      )
    }
    throw new ApiError(message, status, code, errors)
  }

  return (json ? json.data : null) as T
}

/** 信封顶层 warnings（设备创建/更新时 Agent 离线警告，契约 §4.3）。 */
export async function apiFetchWithWarnings<T>(
  path: string,
  options: RequestOptions = {},
): Promise<{ data: T; warnings: Array<{ code: string; message: string }> }> {
  const init: RequestInit = {
    method: options.method ?? 'GET',
    credentials: 'same-origin',
    signal: options.signal,
  }
  if (options.body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(options.body)
  }
  const resp = await fetch(path, init)
  const text = await resp.text()
  const json = text ? (JSON.parse(text) as Envelope<T>) : null
  if (resp.status === 401) {
    onUnauthorized?.()
    throw new ApiError(json?.message || '未登录或登录已过期', 401, 401)
  }
  if (!resp.ok || (json && json.code !== 0)) {
    const message = json?.message || `请求失败（HTTP ${resp.status}）`
    let errors: FieldError[] = []
    if (json && json.data && typeof json.data === 'object') {
      const d = json.data as unknown as { errors?: FieldError[] }
      if (Array.isArray(d.errors)) errors = d.errors
    }
    throw new ApiError(message, resp.status, json?.code ?? resp.status, errors)
  }
  return { data: (json ? json.data : null) as T, warnings: json?.warnings ?? [] }
}
