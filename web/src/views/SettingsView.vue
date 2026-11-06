<template>
  <div class="space-y-6">
    <h1 class="text-xl font-bold">设置</h1>

    <!-- 外观 -->
    <section class="card p-4">
      <div class="section-heading"><div><h2>外观与主题</h2><p>主题偏好仅保存在当前浏览器，不影响其他用户。</p></div></div>
      <div class="theme-choice-grid" role="group" aria-label="选择主题">
        <button class="theme-choice" :class="{ active: ui.theme === 'light' }" @click="ui.setTheme('light')"><span class="theme-preview preview-light">☀</span><strong>浅色</strong><small>清爽明亮</small></button>
        <button class="theme-choice" :class="{ active: ui.theme === 'dark' }" @click="ui.setTheme('dark')"><span class="theme-preview preview-dark">☾</span><strong>深色</strong><small>低光环境</small></button>
        <button class="theme-choice" :class="{ active: ui.theme === 'system' }" @click="ui.setTheme('system')"><span class="theme-preview preview-system">◐</span><strong>跟随系统</strong><small>自动适配设备</small></button>
      </div>
    </section>

    <!-- 账号与安全 -->
    <section class="card p-4">
      <h2 class="text-sm font-semibold">账号与安全</h2>
      <dl class="mt-3 grid grid-cols-1 gap-y-1 text-sm sm:grid-cols-2">
        <dt class="text-slate-500 dark:text-slate-400">当前管理员</dt>
        <dd>{{ auth.me?.username ?? '—' }}（{{ auth.me?.role ?? '' }}）</dd>
        <dt class="text-slate-500 dark:text-slate-400">本次会话签发</dt>
        <dd>{{ formatTime(auth.me?.issued_at) }}</dd>
        <dt class="text-slate-500 dark:text-slate-400">会话过期</dt>
        <dd>{{ formatTime(auth.me?.expires_at) }}（24 小时固定有效期，到期须重新登录）</dd>
      </dl>

      <form class="mt-5 space-y-3" @submit.prevent="changePassword">
        <h3 class="text-sm font-medium">修改密码</h3>
        <p class="text-xs text-slate-500 dark:text-slate-400">
          改密后全部会话立即失效（含当前会话），须重新登录。忘记密码时用服务器本机的
          <code class="font-mono">wakehub-server reset-password</code> 命令重置。
        </p>
        <label class="field">
          <span>当前密码 *</span>
          <input v-model="pw.current" class="input" type="password" required autocomplete="current-password" />
          <span v-if="pwErrors.current_password" class="field-error">{{ pwErrors.current_password }}</span>
        </label>
        <label class="field">
          <span>新密码 *（至少 8 位，不得与用户名相同、不得为默认密码）</span>
          <input v-model="pw.next" class="input" type="password" required autocomplete="new-password" />
          <span v-if="pwErrors.new_password" class="field-error">{{ pwErrors.new_password }}</span>
        </label>
        <label class="field">
          <span>确认新密码 *</span>
          <input v-model="pw.confirm" class="input" type="password" required autocomplete="new-password" />
        </label>
        <p v-if="pwMessage" class="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-950 dark:text-rose-300">{{ pwMessage }}</p>
        <button type="submit" class="btn-primary min-h-[44px]" :disabled="pwBusy">
          {{ pwBusy ? '修改中…' : '修改密码并重新登录' }}
        </button>
      </form>
    </section>

    <!-- 系统信息 -->
    <section class="card p-4">
      <h2 class="text-sm font-semibold">系统信息</h2>
      <dl class="mt-3 grid grid-cols-1 gap-y-1 text-sm sm:grid-cols-2">
        <dt class="text-slate-500 dark:text-slate-400">服务端版本</dt>
        <dd>v{{ stats?.version ?? '—' }}</dd>
        <dt class="text-slate-500 dark:text-slate-400">服务状态</dt>
        <dd>{{ stats?.server_status ?? '—' }}</dd>
        <dt class="text-slate-500 dark:text-slate-400">数据库状态</dt>
        <dd>{{ stats?.db_status ?? '—' }}</dd>
        <dt class="text-slate-500 dark:text-slate-400">已运行</dt>
        <dd>{{ stats ? formatUptime(stats.uptime_seconds) : '—' }}</dd>
        <dt class="text-slate-500 dark:text-slate-400">设备 / Agent</dt>
        <dd>{{ stats?.total_devices ?? 0 }} 台设备 · {{ stats?.total_agents ?? 0 }} 个 Agent（{{ stats?.online_agents ?? 0 }} 在线）</dd>
      </dl>
    </section>

    <!-- 数据备份 -->
    <section class="card p-4">
      <h2 class="text-sm font-semibold">数据备份</h2>
      <p class="mt-2 text-sm text-slate-600 dark:text-slate-300">
        备份与恢复由服务器本机的 <code class="font-mono">scripts/backup.sh</code> 执行（联机备份数据库与配置，恢复前自动保留当前库副本）。
        v0.2.0 不提供网页端备份下载接口。
      </p>
    </section>

    <!-- 关于 -->
    <section class="card p-4">
      <h2 class="text-sm font-semibold">关于</h2>
      <p class="mt-2 text-sm text-slate-600 dark:text-slate-300">
        WakeHub v0.2.11：自托管跨网络设备唤醒管理平台。登录态为 HttpOnly Cookie（不存 localStorage）；
        睡眠端点无签名认证，仅限可信局域网内使用；真主动 ARP 扫描需特权，当前为 TCP 轻触 + ARP 缓存发现。
      </p>
      <button class="btn-danger mt-4 min-h-[44px]" @click="logout">退出登录</button>
    </section>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api'
import { ApiError, type DashboardStats } from '../api/types'
import { useAuthStore } from '../stores/auth'
import { useUiStore } from '../stores/ui'
import { formatTime, formatUptime } from '../utils/format'
import { passwordError } from '../utils/validate'

const auth = useAuthStore()
const ui = useUiStore()
const router = useRouter()

const stats = ref<DashboardStats | null>(null)
const pw = reactive({ current: '', next: '', confirm: '' })
const pwErrors = reactive<Record<string, string>>({})
const pwMessage = ref('')
const pwBusy = ref(false)

async function changePassword() {
  pwMessage.value = ''
  Object.keys(pwErrors).forEach((k) => delete pwErrors[k])
  const err = passwordError(pw.next, auth.me?.username ?? '')
  if (err) {
    pwErrors.new_password = err
    return
  }
  if (pw.next !== pw.confirm) {
    pwErrors.new_password = '两次输入的新密码不一致'
    return
  }
  pwBusy.value = true
  try {
    await auth.changePassword(pw.current, pw.next)
    ui.toast('密码已修改，请重新登录', 'success')
    await router.push({ name: 'login' })
  } catch (e) {
    if (e instanceof ApiError) {
      for (const fe of e.errors) pwErrors[fe.field] = fe.message
      if (e.errors.length === 0) pwMessage.value = e.message
    } else {
      pwMessage.value = '修改失败，请稍后重试'
    }
  } finally {
    pwBusy.value = false
  }
}

async function logout() {
  const ok = await ui.confirm({
    title: '退出登录',
    message: '确认退出当前会话吗？',
    confirmLabel: '退出登录',
  })
  if (!ok) return
  await auth.logout()
}

onMounted(() => {
  void api.dashboard().then((s) => {
    stats.value = s
  }).catch(() => {
    // 系统信息加载失败不阻塞设置页
  })
})
</script>
