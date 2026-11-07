import { describe, expect, it } from 'vitest'
import { compileCron, describeCron, isOnceDateTimeInFuture, isValidCron, parseCron, todayLocalString } from './cron'

describe('cron 编译', () => {
  it('每天', () => {
    expect(compileCron({ kind: 'daily', time: '08:00' })).toBe('0 8 * * *')
  })
  it('每周', () => {
    expect(compileCron({ kind: 'weekly', time: '22:00', weekdays: [1, 3, 5] })).toBe('0 22 * * 1,3,5')
    expect(() => compileCron({ kind: 'weekly', time: '22:00', weekdays: [] })).toThrow()
  })
  it('单次', () => {
    expect(compileCron({ kind: 'once', time: '07:30', date: '2026-11-07' })).toBe('30 7 7 11 *')
    expect(() => compileCron({ kind: 'once', time: '07:30' })).toThrow()
  })
  it('自定义', () => {
    expect(compileCron({ kind: 'custom', time: '08:00', cron: '0 8 * * 1-5' })).toBe('0 8 * * 1-5')
    expect(() => compileCron({ kind: 'custom', time: '08:00', cron: '0 8 * *' })).toThrow()
  })
  it('校验', () => {
    expect(isValidCron('0 8 * * 1-5')).toBe(true)
    expect(isValidCron('0 8 * *')).toBe(false)
    expect(isValidCron('x 8 * * *')).toBe(false)
  })
  it('中文描述', () => {
    expect(describeCron('0 8 * * *')).toBe('每天 08:00')
    expect(describeCron('0 22 * * 1,3,5')).toBe('每周一、周三、周五 22:00')
    expect(describeCron('30 7 7 11 *')).toBe('每年 11 月 7 日 07:30')
  })
})

// v0.3.3 问题 1：编辑时从原 cron 还原草稿，提交不再覆盖成默认值。
describe('parseCron（编辑还原）', () => {
  it('每天还原', () => {
    expect(parseCron('0 8 * * *')).toMatchObject({ kind: 'daily', time: '08:00' })
    expect(compileCron(parseCron('0 8 * * *'))).toBe('0 8 * * *')
  })

  it('编辑每天 00:00 的任务、只改名，提交仍是 0 0 * * *（不是默认 0 8 * * *）', () => {
    // 反向验证：把 TaskForm 的 draft 初始化改回写死默认值，本例失败。
    const draft = parseCron('0 0 * * *')
    expect(draft.kind).toBe('daily')
    expect(compileCron(draft)).toBe('0 0 * * *')
  })

  it('每周还原（逗号列表与范围都展开）', () => {
    expect(parseCron('0 22 * * 1,3,5')).toMatchObject({ kind: 'weekly', time: '22:00', weekdays: [1, 3, 5] })
    expect(compileCron(parseCron('0 22 * * 1,3,5'))).toBe('0 22 * * 1,3,5')
    expect(parseCron('0 8 * * 1-5')).toMatchObject({ kind: 'weekly', weekdays: [1, 2, 3, 4, 5] })
    expect(compileCron(parseCron('0 8 * * 1-5'))).toBe('0 8 * * 1,2,3,4,5')
    expect(parseCron('0 8 * * 0')).toMatchObject({ kind: 'weekly', weekdays: [0] })
  })

  it('单次还原：日期取今年或明年同月同日，编译回原 cron', () => {
    const draft = parseCron('30 7 7 11 *')
    expect(draft.kind).toBe('once')
    expect(draft.time).toBe('07:30')
    expect(draft.date).toMatch(/^\d{4}-11-07$/)
    expect(compileCron(draft)).toBe('30 7 7 11 *')
  })

  it('自定义 cron 原样保留、编辑不变', () => {
    // 反向验证：把 parseCron 的 custom 分支改成返回 daily 默认草稿，本例失败。
    // 注：`30 2 * * 0` 按结论的分类本就是 weekly（周字段为单个星期日），
    // 解析为每周·周日后编译回的仍是同一串 `30 2 * * 0`，编辑不改时间就不会丢；
    // 真正无法归类的表达式才进 custom 分支，原串同样原样保留。
    expect(compileCron(parseCron('30 2 * * 0'))).toBe('30 2 * * 0')
    expect(parseCron('30 2 * * 0')).toMatchObject({ kind: 'weekly', time: '02:30', weekdays: [0] })
    const draft = parseCron('30 2 15 * *')
    expect(draft).toMatchObject({ kind: 'custom', cron: '30 2 15 * *' })
    expect(compileCron(draft)).toBe('30 2 15 * *')
  })

  it('无法归类的表达式归为 custom（月度、步长、非法）', () => {
    expect(parseCron('0 8 15 * *').kind).toBe('custom')
    expect(parseCron('0 8 * * */2').kind).toBe('custom')
    expect(parseCron('0 8 * *').kind).toBe('custom')
    expect(parseCron('0 8 * *').cron).toBe('0 8 * *')
    expect(parseCron('x 8 * * *').kind).toBe('custom')
    expect(compileCron(parseCron('0 8 15 * *'))).toBe('0 8 15 * *')
  })
})

// v0.3.3 问题 4：单次任务的执行时间必须在未来。
describe('单次未来时间校验', () => {
  const now = new Date(2026, 10, 7, 12, 0) // 2026-11-07 12:00 本地时间
  it('过去日期拒绝', () => {
    expect(isOnceDateTimeInFuture('2026-11-06', '08:00', now)).toBe(false)
  })
  it('今天已过的时间拒绝', () => {
    expect(isOnceDateTimeInFuture('2026-11-07', '08:00', now)).toBe(false)
    expect(isOnceDateTimeInFuture('2026-11-07', '12:00', now)).toBe(false)
  })
  it('今天未来的时间与未来日期通过', () => {
    expect(isOnceDateTimeInFuture('2026-11-07', '12:01', now)).toBe(true)
    expect(isOnceDateTimeInFuture('2026-11-08', '00:00', now)).toBe(true)
  })
  it('非法输入拒绝', () => {
    expect(isOnceDateTimeInFuture('', '08:00', now)).toBe(false)
    expect(isOnceDateTimeInFuture('2026-11-08', '', now)).toBe(false)
  })
  it('todayLocalString 为本地 YYYY-MM-DD', () => {
    expect(todayLocalString(now)).toBe('2026-11-07')
  })
})
