import { describe, expect, it } from 'vitest'
import {
  cidrWithinAllowed,
  isPrivateIPv4,
  isValidCIDR,
  isValidIPv4,
  isValidMac,
  isValidPort,
  isValidScanCIDR,
  normalizeMacInput,
  passwordError,
} from './validate'

describe('MAC 校验与规范化', () => {
  it('接受标准 MAC', () => {
    expect(isValidMac('AA:BB:CC:DD:EE:FF')).toBe(true)
    expect(isValidMac('aa-bb-cc-dd-ee-ff')).toBe(true)
  })
  it('拒绝非法 MAC', () => {
    expect(isValidMac('AA:BB:CC:DD:EE')).toBe(false)
    expect(isValidMac('GG:BB:CC:DD:EE:FF')).toBe(false)
    expect(isValidMac('')).toBe(false)
  })
  it('输入时自动补冒号并大写', () => {
    expect(normalizeMacInput('aabbccddeeff')).toBe('AA:BB:CC:DD:EE:FF')
    expect(normalizeMacInput('aa-bb-cc')).toBe('AA:BB:CC')
    expect(normalizeMacInput('AA:BB:CC:DD:EE:FF:00')).toBe('AA:BB:CC:DD:EE:FF')
  })
})

describe('IP 与端口', () => {
  it('IPv4 边界', () => {
    expect(isValidIPv4('192.168.1.29')).toBe(true)
    expect(isValidIPv4('256.0.0.1')).toBe(false)
    expect(isValidIPv4('10.0.0')).toBe(false)
    expect(isValidIPv4('::1')).toBe(false)
  })
  it('端口 1–65535', () => {
    expect(isValidPort(9)).toBe(true)
    expect(isValidPort(65535)).toBe(true)
    expect(isValidPort(0)).toBe(false)
    expect(isValidPort(65536)).toBe(false)
    expect(isValidPort('9')).toBe(true)
  })
  it('私有地址判定（含回环，契约 §6.1）', () => {
    expect(isPrivateIPv4('192.168.1.1')).toBe(true)
    expect(isPrivateIPv4('172.16.0.1')).toBe(true)
    expect(isPrivateIPv4('172.32.0.1')).toBe(false)
    expect(isPrivateIPv4('192.168.1.1')).toBe(true)
    expect(isPrivateIPv4('127.0.0.1')).toBe(true)
    expect(isPrivateIPv4('8.8.8.8')).toBe(false)
    expect(isPrivateIPv4('169.254.169.254')).toBe(false)
  })
})

describe('扫描 CIDR', () => {
  it('合法 CIDR', () => {
    expect(isValidCIDR('192.168.1.0/24')).toBe(true)
    expect(isValidCIDR('192.168.1.0/33')).toBe(false)
    expect(isValidCIDR('192.168.1.0')).toBe(false)
  })
  it('扫描掩码不小于 /24', () => {
    expect(isValidScanCIDR('192.168.1.0/24')).toBe(true)
    expect(isValidScanCIDR('192.168.1.5/32')).toBe(true)
    expect(isValidScanCIDR('192.168.1.0/16')).toBe(false)
    expect(isValidScanCIDR('8.8.8.0/24')).toBe(false)
  })
  it('白名单判定：等于或子网通过，超网/他网拒绝', () => {
    expect(cidrWithinAllowed('192.168.1.0/24', ['192.168.1.0/24'])).toBe(true)
    expect(cidrWithinAllowed('192.168.1.0/25', ['192.168.1.0/24'])).toBe(true)
    expect(cidrWithinAllowed('192.168.1.0/16', ['192.168.1.0/24'])).toBe(false)
    expect(cidrWithinAllowed('192.168.2.0/24', ['192.168.1.0/24'])).toBe(false)
  })
})

describe('密码强度', () => {
  it('弱密码被拦', () => {
    expect(passwordError('short', 'admin')).not.toBeNull()
    expect(passwordError('wakehub123', 'admin')).not.toBeNull()
    expect(passwordError('admin', 'admin')).not.toBeNull()
    expect(passwordError('Str0ng-Pass!', 'admin')).toBeNull()
  })
})
