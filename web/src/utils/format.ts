// 时间与展示格式化。后端时间一律 RFC 3339 UTC，UI 显示本地时区（契约 §1.1）。

export function formatTime(iso: string | null | undefined, timeZone?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(d)
}

export function formatTimeShort(iso: string | null | undefined, timeZone?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone,
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(d)
}

export function formatDurationMs(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return '—'
  if (ms < 1000) return `${ms} ms`
  return `${(ms / 1000).toFixed(1)} s`
}

export function formatUptime(seconds: number): string {
  if (seconds < 60) return `${seconds} 秒`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟`
  if (seconds < 86400) {
    const h = Math.floor(seconds / 3600)
    const m = Math.floor((seconds % 3600) / 60)
    return `${h} 小时 ${m} 分钟`
  }
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  return `${d} 天 ${h} 小时`
}

export function formatMac(mac: string): string {
  return mac.toUpperCase()
}

/** datetime-local 输入值（本地时区）转 RFC 3339 UTC，供 logs 筛选使用。 */
export function localInputToRFC3339(value: string): string {
  if (!value) return ''
  return new Date(value).toISOString()
}
