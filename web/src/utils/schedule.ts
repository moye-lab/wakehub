// 定时任务动作选项与请求体组装（v0.3.2 起开放 sleep 定时关机）。
// 抽成纯函数，便于 vitest 覆盖动作透传与默认值；组件只负责绑定。

export const ACTION_OPTIONS = [
  { value: 'wake', label: '唤醒' },
  { value: 'sleep', label: '睡眠（关机）' },
] as const

export type ScheduleAction = 'wake' | 'sleep'

export interface ScheduleFormInput {
  name: string
  cron_expr: string
  timezone: string
  target_type: 'device' | 'group'
  target_id: string
  action: ScheduleAction | string
  retry_count: number
  run_once: boolean
  enabled: boolean
}

// actionLabel 返回任务动作的展示文案（任务列表徽标用）。
export function actionLabel(action: string): string {
  return action === 'sleep' ? '睡眠（关机）' : '唤醒'
}

/**
 * 时间模式切换时 run_once 复选框的联动（v0.3.3 问题 4：单次任务必须执行后自动禁用）。
 * 切到 once：强制勾选，并记住切入前非 once 模式下的值；切离 once：恢复记住的值。
 * 返回提交给表单的新 run_once 值与更新后的“记住值”。
 */
export function nextRunOnce(
  kind: 'once' | 'daily' | 'weekly' | 'custom',
  currentRunOnce: boolean,
  savedNonOnceRunOnce: boolean,
): { runOnce: boolean; savedNonOnceRunOnce: boolean } {
  if (kind === 'once') return { runOnce: true, savedNonOnceRunOnce: currentRunOnce }
  return { runOnce: savedNonOnceRunOnce, savedNonOnceRunOnce }
}

// buildScheduleBody 组装新建/更新定时任务的请求体：
// action 原样透传；空值默认 wake（与后端“空默认 wake”一致）。
export function buildScheduleBody(input: ScheduleFormInput): Record<string, unknown> {
  return {
    name: input.name.trim(),
    cron_expr: input.cron_expr,
    timezone: input.timezone,
    target_type: input.target_type,
    target_id: input.target_id,
    action: input.action || 'wake',
    retry_count: input.retry_count,
    run_once: input.run_once,
    enabled: input.enabled,
  }
}
