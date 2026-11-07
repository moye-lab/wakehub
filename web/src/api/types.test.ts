import { describe, expect, it } from 'vitest'
import { formatNetworkLabel, sshSudoApplies, type Network } from './types'

function network(over: Partial<Network>): Network {
  return {
    id: 'net-192-168-1-0-24',
    name: '192.168.1.0/24',
    cidr: '192.168.1.0/24',
    description: '',
    created_at: '',
    updated_at: '',
    ...over,
  }
}

describe('formatNetworkLabel', () => {
  it('名字与网段相同时只显示名字', () => {
    expect(formatNetworkLabel(network({}))).toBe('192.168.1.0/24')
  })

  it('名字与网段不同时显示名字（网段）', () => {
    expect(formatNetworkLabel(network({ name: '办公室' }))).toBe('办公室（192.168.1.0/24）')
  })

  it('无网段时只显示名字', () => {
    expect(formatNetworkLabel(network({ name: '默认网络', cidr: '' }))).toBe('默认网络')
  })
})

describe('sshSudoApplies', () => {
  it('只对 SSH 的 Linux 目标适用', () => {
    expect(sshSudoApplies('ssh', 'linux')).toBe(true)
  })

  it('Windows 目标不适用', () => {
    expect(sshSudoApplies('ssh', 'windows')).toBe(false)
  })

  it('非 SSH 适配器不适用', () => {
    expect(sshSudoApplies('http_sol', 'linux')).toBe(false)
    expect(sshSudoApplies('none', 'linux')).toBe(false)
  })
})
