<template>
  <div class="flex min-h-screen items-center justify-center p-4">
    <div class="card w-full max-w-md p-6 shadow-xl">
      <h1 class="text-2xl font-bold">WakeHub</h1>
      <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">自托管跨网络设备唤醒管理平台</p>
      <form class="mt-6 space-y-4" @submit.prevent="submit">
        <label class="field">
          <span>管理员账号</span>
          <input
            v-model="form.username"
            class="input"
            type="text"
            required
            autocomplete="username"
            placeholder="请输入管理员账号"
          />
        </label>
        <label class="field">
          <span>密码</span>
          <input
            v-model="form.password"
            class="input"
            type="password"
            required
            autocomplete="current-password"
            placeholder="请输入密码"
          />
        </label>
        <p v-if="error" class="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-950 dark:text-rose-300">
          {{ error }}
        </p>
        <button type="submit" class="btn-primary min-h-[48px] w-full" :disabled="submitting">
          {{ submitting ? '登录中…' : '安全登录' }}
        </button>
      </form>
      <p class="mt-4 text-center text-xs text-slate-400 dark:text-slate-500">
        登录态为 HttpOnly Cookie，会话 24 小时有效；首次部署请按安装文档修改默认密码。
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ApiError } from '../api/types'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const router = useRouter()

const form = reactive({ username: '', password: '' })
const error = ref('')
const submitting = ref(false)

async function submit() {
  if (submitting.value) return
  error.value = ''
  submitting.value = true
  try {
    await auth.login(form.username.trim(), form.password)
    await router.push({ name: 'dashboard' })
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : '登录失败，请稍后重试'
  } finally {
    submitting.value = false
  }
}
</script>
