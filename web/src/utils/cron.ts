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
