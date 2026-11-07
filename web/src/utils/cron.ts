// 定时任务可视化创建器 → cron（契约 §8.1）。
// schedule_kind（单次/每日/每周）是前端概念，不进 API；
// 前端把选择编译为 cron 表达式再提交，API 只收 cron。

export type ScheduleKind = 'once' | 'daily' | 'weekly' | 'custom'

export interface ScheduleDraft {
  kind: ScheduleKind
  /** HH:mm */
  time: string
  /** once: YYYY-MM-DD；weekly: 0-6（0=周日）多选 */
  date?: string
  weekdays?: number[]
  /** custom 时直接填 cron */
  cron?: string
}

const CRON_FIELD = /^(\*|\d+)([-/,]\d+|\/\d+|-\d+)?(,(\*|\d+)([-/,]\d+|\/\d+|-\d+)?)*$/

export function isValidCron(expr: string): boolean {
  const parts = expr.trim().split(/\s+/)
  if (parts.length !== 5) return false
  return parts.every((p) => CRON_FIELD.test(p))
}

/** 把 cron 还原为时间草稿（v0.3.3 问题 1：编辑时不再用写死默认值覆盖原 cron）。
 *  daily：`m h * * *`；weekly：`m h * * <dow 列表/范围>`；once：`m h <日> <月> *`；
 *  其余（月度、带步长/列表的日月、非法表达式等）一律归为 custom，原串原样保留，
 *  提交时 compileCron 直接回传，编辑不改时间就不会丢表达式。 */
export function parseCron(expr: string): ScheduleDraft {
  const custom = (): ScheduleDraft => ({ kind: 'custom', time: '08:00', cron: expr })
  const parts = expr.trim().split(/\s+/)
  if (parts.length !== 5) return custom()
  const [m, h, dom, mon, dow] = parts
  const isNum = (s: string) => /^\d+$/.test(s)
  if (!isNum(m) || !isNum(h)) return custom()
  const minute = Number(m)
  const hour = Number(h)
  if (minute < 0 || minute > 59 || hour < 0 || hour > 23) return custom()
  const time = `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`
  if (dom === '*' && mon === '*' && dow === '*') {
    return { kind: 'daily', time, weekdays: [1, 2, 3, 4, 5] }
  }
  if (dom === '*' && mon === '*' && dow !== '*') {
    const weekdays = parseDowField(dow)
    if (weekdays === null) return custom()
    return { kind: 'weekly', time, weekdays }
  }
  if (dom !== '*' && mon !== '*' && dow === '*' && isNum(dom) && isNum(mon)) {
    const day = Number(dom)
    const month = Number(mon)
    if (day < 1 || day > 31 || month < 1 || month > 12) return custom()
    // cron 本身无年份：草稿日期取今年；若今年该日期+时间已过，取明年，
    // 使编辑后提交仍能通过“单次须在未来”校验，且 compileCron 忽略年份、cron 不变。
    const now = new Date()
    let year = now.getFullYear()
    const candidate = new Date(year, month - 1, day, hour, minute)
    if (candidate.getTime() <= now.getTime()) year += 1
    const date = `${year}-${String(month).padStart(2, '0')}-${String(day).padStart(2, '0')}`
    return { kind: 'once', time, date }
  }
  return custom()
}

/** 解析周字段：逗号列表与 a-b 范围展开为 0-6 的星期集合；其余写法（步长等）返回 null。 */
function parseDowField(field: string): number[] | null {
  const out = new Set<number>()
  for (const tok of field.split(',')) {
    if (/^\d+$/.test(tok)) {
      const n = Number(tok)
      if (n < 0 || n > 6) return null
      out.add(n)
    } else {
      const range = /^(\d+)-(\d+)$/.exec(tok)
      if (!range) return null
      const a = Number(range[1])
      const b = Number(range[2])
      if (a < 0 || b > 6 || a > b) return null
      for (let d = a; d <= b; d += 1) out.add(d)
    }
  }
  return [...out].sort((a, b) => a - b)
}

/** 本地日期 YYYY-MM-DD（供单次日期 input 的 min 与默认值）。 */
export function todayLocalString(now = new Date()): string {
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
}

/** 单次任务的日期+时间（本地时间）是否在未来（v0.3.3 问题 4 前端校验）。 */
export function isOnceDateTimeInFuture(date: string, time: string, now = new Date()): boolean {
  const dm = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date ?? '')
  const tm = /^(\d{2}):(\d{2})$/.exec(time ?? '')
  if (!dm || !tm) return false
  const dt = new Date(Number(dm[1]), Number(dm[2]) - 1, Number(dm[3]), Number(tm[1]), Number(tm[2]))
  return dt.getTime() > now.getTime()
}

export function compileCron(d: ScheduleDraft): string {
  const [hh, mm] = d.time.split(':')
  const minute = String(Number(mm))
  const hour = String(Number(hh))
  switch (d.kind) {
    case 'once': {
      if (!d.date) throw new Error('单次任务须选择日期')
      const [, m, day] = d.date.split('-').map(Number)
      return `${minute} ${hour} ${day} ${m} *`
    }
    case 'daily':
      return `${minute} ${hour} * * *`
    case 'weekly': {
      const days = (d.weekdays ?? []).slice().sort((a, b) => a - b)
      if (days.length === 0) throw new Error('每周任务至少选择一天')
      return `${minute} ${hour} * * ${days.join(',')}`
    }
    case 'custom': {
      const cron = (d.cron ?? '').trim()
      if (!isValidCron(cron)) throw new Error('Cron 表达式格式错误（须为 5 段：分 时 日 月 周）')
      return cron
    }
  }
}

/** 把 cron 粗略翻回中文描述（仅用于列表展示，复杂表达式原样显示）。 */
export function describeCron(expr: string): string {
  const parts = expr.trim().split(/\s+/)
  if (parts.length !== 5) return expr
  const [m, h, dom, mon, dow] = parts
  const time = `${h.padStart(2, '0')}:${m.padStart(2, '0')}`
  const weekdayNames = ['日', '一', '二', '三', '四', '五', '六']
  if (dom !== '*' && mon !== '*') return `每年 ${mon} 月 ${dom} 日 ${time}`
  if (dom !== '*') return `每月 ${dom} 日 ${time}`
  if (dow !== '*') {
    const days = dow
      .split(',')
      .map((d) => `周${weekdayNames[Number(d)] ?? d}`)
      .join('、')
    return `每${days} ${time}`
  }
  return `每天 ${time}`
}

export const WEEKDAY_OPTIONS = [
  { value: 1, label: '周一' },
  { value: 2, label: '周二' },
  { value: 3, label: '周三' },
  { value: 4, label: '周四' },
  { value: 5, label: '周五' },
  { value: 6, label: '周六' },
  { value: 0, label: '周日' },
]

export const TIMEZONE_OPTIONS = [
  'UTC',
  'Asia/Shanghai',
  'Asia/Taipei',
  'Asia/Hong_Kong',
  'Asia/Tokyo',
  'Asia/Seoul',
  'Asia/Singapore',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Los_Angeles',
  'Australia/Sydney',
]
