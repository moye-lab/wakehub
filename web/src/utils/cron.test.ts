import { describe, expect, it } from 'vitest'
import { compileCron, describeCron, isValidCron } from './cron'

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
