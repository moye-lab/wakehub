// 前端即时校验（契约 §4.1、§7.2）。后端是最终裁决，前端只做“当场拦住、
// 当场提示”，不把前端校验当成安全边界。

const MAC_RE = /^([0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}$/
const IPV4_RE = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/

export function isValidMac(value: string): boolean {
  return MAC_RE.test(value.trim())
}

/** 输入时自动规范化：去分隔符、补冒号、大写（AA:BB:CC:DD:EE:FF）。 */
export function normalizeMacInput(value: string): string {
  const hex = value.replace(/[^0-9A-Fa-f]/g, '').toUpperCase().slice(0, 12)
  const groups: string[] = []
  for (let i = 0; i < hex.length; i += 2) groups.push(hex.slice(i, i + 2))
  return groups.join(':')
}

export function isValidIPv4(value: string): boolean {
  return IPV4_RE.test(value.trim())
}

export function isValidPort(value: number | string): boolean {
  const n = typeof value === 'string' ? Number(value) : value
  return Number.isInteger(n) && n >= 1 && n <= 65535
}

export function isValidCIDR(value: string): boolean {
  const m = value.trim().match(/^(.+)\/(\d+)$/)
  if (!m) return false
  if (!isValidIPv4(m[1])) return false
  const mask = Number(m[2])
  return mask >= 0 && mask <= 32
}

/** 私有 IPv4（10/8、172.16/12、192.168/16）或回环 127/8（契约 §6.1）。 */
export function isPrivateIPv4(value: string): boolean {
  if (!isValidIPv4(value)) return false
  const [a, b] = value.split('.').map(Number)
  if (a === 10) return true
  if (a === 172 && b >= 16 && b <= 31) return true
  if (a === 192 && b === 168) return true
  if (a === 127) return true
  return false
}

/** 扫描 CIDR：私有网段且掩码 ≥ /24（契约 §7.2，/32 单主机允许）。 */
export function isValidScanCIDR(value: string): boolean {
  if (!isValidCIDR(value)) return false
  const [ip, mask] = value.trim().split('/')
  if (Number(mask) < 24) return false
  return isPrivateIPv4(ip)
}

/** CIDR 是否在白名单内（等于某一项或是其子网）。 */
export function cidrWithinAllowed(cidr: string, allowed: string[]): boolean {
  if (!isValidCIDR(cidr)) return false
  const [ip, maskStr] = cidr.trim().split('/')
  const mask = Number(maskStr)
  const ipNum = ipv4ToNum(ip)
  for (const a of allowed) {
    if (!isValidCIDR(a)) continue
    const [aip, amaskStr] = a.trim().split('/')
    const amask = Number(amaskStr)
    if (mask < amask) continue
    if ((ipNum & maskBits(amask)) === (ipv4ToNum(aip) & maskBits(amask))) return true
  }
  return false
}

function ipv4ToNum(ip: string): number {
  return ip.split('.').reduce((acc, part) => (acc << 8) + Number(part), 0) >>> 0
}

function maskBits(mask: number): number {
  return mask === 0 ? 0 : (0xffffffff << (32 - mask)) >>> 0
}

/** 新密码强度（契约 §2.4）：≥8 位、不得与用户名相同、不得为已知占位值。 */
export function passwordError(pw: string, username: string): string | null {
  if (pw.length < 8) return '新密码长度至少 8 位'
  if (pw === username) return '新密码不得与用户名相同'
  if (pw === 'wakehub123') return '不得使用默认密码'
  return null
}
