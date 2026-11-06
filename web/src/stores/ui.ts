// 全局 UI 状态：主题偏好、Toast 与危险操作确认。
import { defineStore } from 'pinia'
import { ref } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'system'
export type ToastKind = 'success' | 'error' | 'info'
export interface Toast { id: number; kind: ToastKind; message: string }
export interface ConfirmOptions { title: string; message: string; confirmLabel?: string; danger?: boolean }
let toastSeq = 0
let mediaQuery: MediaQueryList | null = null
let mediaListener: ((event: MediaQueryListEvent) => void) | null = null

export const useUiStore = defineStore('ui', () => {
  const theme = ref<ThemeMode>('dark')
  const toasts = ref<Toast[]>([])
  const confirmState = ref<(ConfirmOptions & { resolve: (v: boolean) => void }) | null>(null)

  function applyTheme() {
    const resolved = theme.value === 'system'
      ? (window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
      : theme.value
    document.documentElement.classList.toggle('dark', resolved === 'dark')
    document.documentElement.dataset.theme = resolved
    document.documentElement.style.colorScheme = resolved
  }
  function initTheme() {
    const saved = localStorage.getItem('wakehub_theme')
    theme.value = saved === 'light' || saved === 'dark' || saved === 'system' ? saved : 'dark'
    applyTheme()
    if (mediaQuery && mediaListener) mediaQuery.removeEventListener('change', mediaListener)
    mediaQuery = window.matchMedia?.('(prefers-color-scheme: dark)') ?? null
    mediaListener = () => { if (theme.value === 'system') applyTheme() }
    mediaQuery?.addEventListener('change', mediaListener)
  }
  function setTheme(value: ThemeMode) { theme.value = value; localStorage.setItem('wakehub_theme', value); applyTheme() }
  function toggleTheme() { setTheme(theme.value === 'dark' ? 'light' : 'dark') }
  function toast(message: string, kind: ToastKind = 'info') {
    const id = ++toastSeq
    toasts.value.push({ id, kind, message })
    window.setTimeout(() => { toasts.value = toasts.value.filter((t) => t.id !== id) }, 4000)
  }
  function confirm(opts: ConfirmOptions): Promise<boolean> { return new Promise((resolve) => { confirmState.value = { ...opts, resolve } }) }
  function resolveConfirm(value: boolean) { confirmState.value?.resolve(value); confirmState.value = null }
  return { theme, toasts, confirmState, initTheme, setTheme, toggleTheme, toast, confirm, resolveConfirm }
})
