<template>
  <div class="fixed inset-0 z-[80] flex items-end justify-center bg-black/60 sm:items-center sm:p-4" @click.self="close">
    <div role="dialog" aria-modal="true" class="card max-h-[92vh] w-full max-w-xl overflow-y-auto p-5">
      <div class="flex items-center justify-between">
        <h2 class="text-lg font-semibold">{{ editing ? '编辑定时任务' : '新建定时任务' }}</h2>
        <button class="btn-ghost min-h-[44px] min-w-[44px]" @click="close" aria-label="关闭">✕</button>
      </div>

      <form class="mt-4 space-y-4" @submit.prevent="submit">
        <label class="field">
          <span>任务名称 *</span>
          <input v-model="form.name" class="input" required maxlength="64" placeholder="如 工作日开机" />
          <span v-if="errors.name" class="field-error">{{ errors.name }}</span>
        </label>

        <fieldset>
          <legend class="mb-2 text-sm font-semibold">执行时间</legend>
          <div class="flex flex-wrap gap-2" role="tablist" aria-label="时间模式">
            <button
              v-for="k in kindOptions"
              :key="k.value"
              type="button"
              role="tab"
              :aria-selected="draft.kind === k.value"
              class="min-h-[44px] rounded-lg border px-3 text-sm"
              :class="draft.kind === k.value
                ? 'border-blue-600 bg-blue-50 text-blue-700 dark:border-blue-500 dark:bg-blue-950 dark:text-blue-300'
                : 'border-slate-300 dark:border-slate-700'"
              @click="draft.kind = k.value"
            >
              {{ k.label }}
            </button>
          </div>

          <div class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label v-if="draft.kind !== 'custom'" class="field">
              <span>时间 *</span>
              <input v-model="draft.time" class="input" type="time" required />
            </label>
            <label v-if="draft.kind === 'once'" class="field">
              <span>日期 *</span>
              <input v-model="draft.date" class="input" type="date" required />
            </label>
            <div v-if="draft.kind === 'weekly'" class="field">
              <span>星期 *</span>
              <div class="flex flex-wrap gap-2">
                <button
                  v-for="w in WEEKDAY_OPTIONS"
                  :key="w.value"
                  type="button"
                  class="min-h-[44px] rounded-lg border px-3 text-sm"
                  :class="draft.weekdays?.includes(w.value)
                    ? 'border-blue-600 bg-blue-50 text-blue-700 dark:border-blue-500 dark:bg-blue-950 dark:text-blue-300'
                    : 'border-slate-300 dark:border-slate-700'"
                  :aria-pressed="draft.weekdays?.includes(w.value)"
                  @click="toggleWeekday(w.value)"
                >
                  {{ w.label }}
                </button>
              </div>
            </div>
            <label v-if="draft.kind === 'custom'" class="field sm:col-span-2">
              <span>Cron 表达式 *（分 时 日 月 周）</span>
              <input v-model="draft.cron" class="input font-mono" placeholder="0 8 * * 1-5" required />
              <span class="field-hint">示例：每天 08:00 → 0 8 * * *；工作日 08:00 → 0 8 * * 1-5</span>
            </label>
          </div>
          <p v-if="compiledCron" class="mt-2 text-xs text-slate-500 dark:text-slate-400">
            将提交的 Cron：<code class="font-mono">{{ compiledCron }}</code>
            <span v-if="draft.kind !== 'custom'">（{{ describeCron(compiledCron) }}）</span>
          </p>
        </fieldset>

        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label class="field">
            <span>时区</span>
            <select v-model="form.timezone" class="input">
              <option v-for="tz in TIMEZONE_OPTIONS" :key="tz" :value="tz">{{ tz }}</option>
            </select>
          </label>
          <label class="field">
            <span>重试次数（0–5）</span>
            <input v-model.number="form.retry_count" class="input" type="number" min="0" max="5" />
          </label>
        </div>

        <fieldset>
          <legend class="mb-2 text-sm font-semibold">目标</legend>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label class="field">
              <span>动作</span>
              <select v-model="form.action" class="input">
                <option v-for="a in ACTION_OPTIONS" :key="a.value" :value="a.value">{{ a.label }}</option>
              </select>
            </label>
            <label class="field">
              <span>目标类型</span>
              <select v-model="form.target_type" class="input">
                <option value="device">单台设备</option>
                <option value="group">设备分组</option>
              </select>
            </label>
            <label class="field">
              <span>目标 *</span>
              <select v-model="form.target_id" class="input" required>
                <option value="" disabled>请选择</option>
                <template v-if="form.target_type === 'device'">
                  <option v-for="d in devices.items" :key="d.id" :value="d.id">{{ d.name }}（{{ d.ip || d.mac }}）</option>
                </template>
                <template v-else>
                  <option v-for="g in devices.groups" :key="g.id" :value="g.id">{{ g.name }}</option>
                </template>
              </select>
              <span v-if="errors.target_id" class="field-error">{{ errors.target_id }}</span>
            </label>
          </div>
          <p v-if="form.action === 'sleep'" class="mt-2 text-xs text-slate-500 dark:text-slate-400">
            睡眠任务按设备配置的关机适配器（SSH / 受控端点）执行关机；未配置适配器的设备会跳过，不重试。
          </p>
        </fieldset>

        <div class="flex flex-wrap gap-4 text-sm">
          <label class="flex items-center gap-2">
            <input v-model="form.enabled" type="checkbox" class="h-5 w-5 accent-blue-600" />
            启用任务
          </label>
          <label class="flex items-center gap-2">
            <input v-model="form.run_once" type="checkbox" class="h-5 w-5 accent-blue-600" />
            单次任务（首次执行后自动禁用）
          </label>
        </div>

        <div v-if="generalError" class="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-950 dark:text-rose-300">
          {{ generalError }}
        </div>

        <div class="flex justify-end gap-2">
          <button type="button" class="btn-secondary min-h-[44px]" @click="close">取消</button>
          <button type="submit" class="btn-primary min-h-[44px]" :disabled="submitting">
            {{ submitting ? '保存中…' : editing ? '保存修改' : '创建任务' }}
          </button>
        </div>
      </form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { ApiError, type Schedule } from '../api/types'
import { useDevicesStore } from '../stores/devices'
import { useTasksStore } from '../stores/tasks'
import { useUiStore } from '../stores/ui'
import { TIMEZONE_OPTIONS, WEEKDAY_OPTIONS, compileCron, describeCron, type ScheduleDraft } from '../utils/cron'
import { ACTION_OPTIONS, buildScheduleBody } from '../utils/schedule'

const props = defineProps<{ editing?: Schedule | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

const devices = useDevicesStore()
const tasks = useTasksStore()
const ui = useUiStore()

const editing = computed(() => !!props.editing)

const kindOptions = [
  { value: 'once', label: '单次' },
  { value: 'daily', label: '每天' },
  { value: 'weekly', label: '每周' },
  { value: 'custom', label: '自定义 Cron' },
] as const

const draft = reactive<ScheduleDraft>({
  kind: 'daily',
  time: '08:00',
  weekdays: [1, 2, 3, 4, 5],
  cron: '0 8 * * 1-5',
})
const form = reactive({
  name: props.editing?.name ?? '',
  timezone: props.editing?.timezone ?? 'Asia/Shanghai',
  retry_count: props.editing?.retry_count ?? 1,
  action: (props.editing?.action === 'sleep' ? 'sleep' : 'wake') as 'wake' | 'sleep',
  target_type: (props.editing?.target_type ?? 'device') as 'device' | 'group',
  target_id: props.editing?.target_id ?? '',
  enabled: props.editing ? props.editing.enabled : true,
  run_once: props.editing?.run_once ?? false,
})
const errors = reactive<Record<string, string>>({})
const generalError = ref('')
const submitting = ref(false)

const compiledCron = computed(() => {
  try {
    return compileCron(draft)
  } catch {
    return ''
  }
})

function toggleWeekday(v: number) {
  const list = draft.weekdays ?? []
  const idx = list.indexOf(v)
  if (idx >= 0) list.splice(idx, 1)
  else list.push(v)
  draft.weekdays = list
}

async function submit() {
  if (submitting.value) return
  generalError.value = ''
  Object.keys(errors).forEach((k) => delete errors[k])
  let cron = ''
  try {
    cron = compileCron(draft)
  } catch (e) {
    generalError.value = e instanceof Error ? e.message : '时间配置错误'
    return
  }
  if (!form.name.trim()) {
    errors.name = '任务名称不能为空'
    return
  }
  if (!form.target_id) {
    errors.target_id = '请选择目标'
    return
  }
  submitting.value = true
  try {
    const body: Record<string, unknown> = buildScheduleBody({
      name: form.name,
      cron_expr: cron,
      timezone: form.timezone,
      target_type: form.target_type,
      target_id: form.target_id,
      action: form.action,
      retry_count: form.retry_count,
      run_once: form.run_once,
      enabled: form.enabled,
    })
    if (editing.value && props.editing) {
      await tasks.update(props.editing.id, body)
      ui.toast('定时任务已更新', 'success')
    } else {
      await tasks.create(body)
      ui.toast('定时任务已创建', 'success')
    }
    emit('saved')
    emit('close')
  } catch (e) {
    if (e instanceof ApiError) {
      for (const fe of e.errors) errors[fe.field] = fe.message
      if (e.errors.length === 0) generalError.value = e.message
    } else {
      generalError.value = '保存失败，请稍后重试'
    }
  } finally {
    submitting.value = false
  }
}

function close() {
  emit('close')
}
</script>
