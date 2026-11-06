// 认证 store：HttpOnly Cookie 会话（契约 §2）。
// 前端不持有、不存储任何 token（F-N1）；登录态由 GET /api/v1/auth/me 判定。
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '../api'
import { setUnauthorizedHandler } from '../api/client'
import type { Me } from '../api/types'

export const useAuthStore = defineStore('auth', () => {
  const me = ref<Me | null>(null)
  const checked = ref(false)
  let redirectToLogin: (() => void) | null = null

  function bindUnauthorized(fn: () => void) {
    redirectToLogin = fn
    setUnauthorizedHandler(() => {
      me.value = null
      fn()
    })
  }

  /** 应用启动时调用：Cookie 有效则恢复登录态，否则保持未登录。 */
  async function restore(): Promise<boolean> {
    try {
      me.value = await api.me()
      return true
    } catch {
      me.value = null
      return false
    } finally {
      checked.value = true
    }
  }

  async function login(username: string, password: string) {
    await api.login(username, password)
    me.value = await api.me()
  }

  async function logout() {
    try {
      await api.logout()
    } finally {
      me.value = null
      redirectToLogin?.()
    }
  }

  async function changePassword(current_password: string, new_password: string) {
    await api.changePassword(current_password, new_password)
    // 改密后全部会话失效（含当前会话），须重新登录（契约 §2.4）。
    me.value = null
  }

  return { me, checked, bindUnauthorized, restore, login, logout, changePassword }
})
