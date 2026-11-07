import { describe, expect, it } from 'vitest'
import { ACTION_OPTIONS, actionLabel, buildScheduleBody, nextRunOnce } from './schedule'

const base = {
  name: '午夜关机',
  cron_expr: '0 0 * * *',
  timezone: 'Asia/Shanghai',
  target_type: 'device' as const,
  target_id: 'dev-1',
  action: 'sleep' as const,
  retry_count: 1,
  run_once: false,
  enabled: true,
}

describe('定时任务动作', () => {
  it('动作选项含唤醒与睡眠（关机）', () => {
    expect(ACTION_OPTIONS.map((a) => a.value)).toEqual(['wake', 'sleep'])
    expect(ACTION_OPTIONS.find((a) => a.value === 'sleep')?.label).toBe('睡眠（关机）')
  })

  it('sleep 原样透传到请求体', () => {
    // 反向验证：把 buildScheduleBody 的 action 改成写死 'wake'，本例失败。
    expect(buildScheduleBody(base).action).toBe('sleep')
  })

  it('wake 原样透传', () => {
    expect(buildScheduleBody({ ...base, action: 'wake' }).action).toBe('wake')
  })

  it('action 为空默认 wake', () => {
    expect(buildScheduleBody({ ...base, action: '' }).action).toBe('wake')
  })

  it('其余字段原样透传且名称去空格', () => {
    const body = buildScheduleBody({ ...base, name: '  午夜关机  ' })
    expect(body.name).toBe('午夜关机')
    expect(body.cron_expr).toBe('0 0 * * *')
    expect(body.target_type).toBe('device')
    expect(body.target_id).toBe('dev-1')
    expect(body.retry_count).toBe(1)
    expect(body.run_once).toBe(false)
    expect(body.enabled).toBe(true)
  })

  it('动作徽标文案', () => {
    expect(actionLabel('sleep')).toBe('睡眠（关机）')
    expect(actionLabel('wake')).toBe('唤醒')
    expect(actionLabel('')).toBe('唤醒')
  })
})

// v0.3.3 问题 4：选 kind=once 时 run_once 自动勾选且禁用（组件用 :disabled 绑定
// draft.kind === 'once'）；切离 once 恢复切入前的值。
describe('单次 run_once 联动', () => {
  it('切到 once 强制勾选，并记住切入前的值', () => {
    expect(nextRunOnce('once', false, false)).toEqual({ runOnce: true, savedNonOnceRunOnce: false })
    expect(nextRunOnce('once', true, false)).toEqual({ runOnce: true, savedNonOnceRunOnce: true })
  })
  it('切离 once 恢复记住的值', () => {
    // 反向验证：把切离分支改成恒返回 false，本例失败。
    expect(nextRunOnce('daily', true, false)).toEqual({ runOnce: false, savedNonOnceRunOnce: false })
    expect(nextRunOnce('weekly', true, true)).toEqual({ runOnce: true, savedNonOnceRunOnce: true })
  })
  it('非 once 之间切换不改变值', () => {
    expect(nextRunOnce('daily', false, false)).toEqual({ runOnce: false, savedNonOnceRunOnce: false })
    expect(nextRunOnce('custom', true, true)).toEqual({ runOnce: true, savedNonOnceRunOnce: true })
  })
})
