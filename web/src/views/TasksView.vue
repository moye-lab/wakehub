<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-xl font-bold">定时任务</h1>
      <button class="btn-primary min-h-[44px]" @click="showForm = true">+ 新建任务</button>
    </div>

    <ErrorState v-if="tasks.error && tasks.items.length > 0" :message="tasks.error" compact @retry="retry" />
    <div v-if="tasks.loading" class="py-8 text-center text-sm text-slate-500">加载中…</div>
    <ErrorState v-else-if="tasks.error" :message="tasks.error" @retry="retry" />
    <EmptyState
      v-else-if="tasks.items.length === 0"
      title="暂无定时任务"
      hint="新建任务可按单次 / 每天 / 每周定时唤醒或关机一台设备或一个分组。"
      icon="⏰"
    />
    <div v-else class="space-y-3">
      <div v-for="t in tasks.items" :key="t.id" class="card p-4">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <span class="font-semibold">{{ t.name }}</span>
              <StatusBadge :tone="t.action === 'sleep' ? 'amber' : 'blue'" :label="actionLabel(t.action)" />
              <StatusBadge :tone="t.enabled ? 'green' : 'gray'" :label="t.enabled ? '已启用' : '已暂停'" />
              <StatusBadge v-if="t.run_once" tone="blue" label="单次任务" />
              <StatusBadge
                v-if="t.last_run_status"
                :tone="t.last_run_status === 'success' ? 'green' : t.last_run_status === 'skipped' ? 'gray' : t.last_run_status === 'partial' ? 'amber' : 'red'"
                :label="`上次：${runStatusLabel(t.last_run_status)}`"
              />
            </div>
            <div class="mt-1 text-xs text-slate-500 dark:text-slate-400">
              {{ describeCron(t.cron_expr) }}（{{ t.cron_expr }}）· {{ t.timezone }} ·
              目标：{{ t.target_name ?? '目标已删' }}（{{ t.target_type === 'device' ? '设备' : '分组' }}）·
              重试 {{ t.retry_count }} 次
            </div>
            <div class="mt-1 text-xs text-slate-500 dark:text-slate-400">
              上次执行：{{ formatTime(t.last_run_at) }} · 下次执行：{{ formatTime(t.next_run_at) }}
            </div>
          </div>
          <div class="flex flex-wrap gap-1.5">
            <button v-if="t.enabled" class="btn-secondary min-h-[44px] px-3" :disabled="tasks.busyIds.has(t.id)" @click="pause(t)">暂停</button>
            <button v-else class="btn-primary min-h-[44px] px-3" :disabled="tasks.busyIds.has(t.id)" @click="resume(t)">恢复</button>
            <button class="btn-secondary min-h-[44px] px-3" @click="openRuns(t)">运行记录</button>
            <button class="btn-secondary min-h-[44px] px-3" @click="edit(t)">编辑</button>
            <button class="btn-danger min-h-[44px] px-3" @click="remove(t)">删除</button>
          </div>
        </div>
      </div>
    </div>

    <Pagination
      :total="tasks.total"
      :page="tasks.page"
      :page-size="tasks.pageSize"
      @change="changePage"
      @size-change="changeSize"
    />

    <TaskForm v-if="showForm" :editing="editingTask" @close="closeForm" @saved="onSaved" />

    <!-- 运行记录抽屉 -->
    <div v-if="tasks.runsTask" class="fixed inset-0 z-[80] flex justify-end bg-black/60" @click.self="tasks.closeRuns()">
      <div role="dialog" aria-modal="true" class="h-full w-full max-w-xl overflow-y-auto bg-white p-5 dark:bg-slate-900">
        <div class="flex items-center justify-between">
          <h2 class="text-lg font-semibold">运行记录 · {{ tasks.runsTask.name }}</h2>
          <button class="btn-ghost min-h-[44px] min-w-[44px]" @click="tasks.closeRuns()" aria-label="关闭">✕</button>
        </div>
        <div v-if="tasks.runsLoading" class="py-8 text-center text-sm text-slate-500">加载中…</div>
        <ErrorState v-else-if="tasks.runsError" :message="tasks.runsError" @retry="reloadRuns" />
        <div v-else-if="!tasks.runs?.items.length" class="py-8 text-center text-sm text-slate-500">暂无运行记录。</div>
        <ul v-else class="mt-4 space-y-3">
          <li v-for="r in tasks.runs.items" :key="r.id" class="rounded-lg bg-slate-50 p-3 dark:bg-slate-800">
            <div class="flex flex-wrap items-center justify-between gap-2 text-sm">
              <StatusBadge
                :tone="r.status === 'success' ? 'green' : r.status === 'skipped' ? 'gray' : r.status === 'partial' ? 'amber' : 'red'"
                :label="runStatusLabel(r.status)"
              />
              <span class="text-xs text-slate-500 dark:text-slate-400">
                {{ formatTime(r.started_at) }} · {{ formatDurationMs(r.duration_ms) }}
              </span>
            </div>
            <p class="mt-2 text-sm">{{ r.output }}</p>
            <ul v-if="r.devices?.length" class="mt-2 space-y-1 text-xs">
              <li v-for="d in r.devices" :key="d.device_id" class="flex justify-between gap-2">
                <span>{{ d.name }}</span>
                <span :class="d.status === 'success' ? 'text-emerald-600 dark:text-emerald-400' : d.status === 'failed' ? 'text-rose-600 dark:text-rose-400' : 'text-slate-500'">
                  {{ d.status === 'success' ? '成功' : d.status === 'failed' ? `失败（${d.reason}）` : `跳过（${d.reason}）` }}
                </span>
              </li>
            </ul>
          </li>
        </ul>
        <div v-if="tasks.runs" class="mt-4">
          <Pagination
            :total="tasks.runs.total"
            :page="tasks.runs.page"
            :page-size="tasks.runs.page_size"
            @change="(p) => tasks.runsTask && tasks.loadRuns(tasks.runsTask, p)"
            @size-change="() => {}"
          />
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ApiError, type Schedule } from '../api/types'
import EmptyState from '../components/EmptyState.vue'
import ErrorState from '../components/ErrorState.vue'
import Pagination from '../components/Pagination.vue'
import StatusBadge from '../components/StatusBadge.vue'
import TaskForm from '../components/TaskForm.vue'
import { useDevicesStore } from '../stores/devices'
import { useTasksStore } from '../stores/tasks'
import { useUiStore } from '../stores/ui'
import { describeCron } from '../utils/cron'
import { formatDurationMs, formatTime } from '../utils/format'
import { actionLabel } from '../utils/schedule'

const tasks = useTasksStore()
const devices = useDevicesStore()
const ui = useUiStore()

const showForm = ref(false)
const editingTask = ref<Schedule | null>(null)

const RUN_STATUS_LABEL: Record<string, string> = {
  success: '成功',
  partial: '部分成功',
  failed: '失败',
  skipped: '跳过',
}
function runStatusLabel(s: string): string {
  return RUN_STATUS_LABEL[s] ?? s
}

function changePage(p: number) {
  tasks.page = p
  void tasks.load()
}

function changeSize(size: number) {
  tasks.pageSize = size
  tasks.page = 1
  void tasks.load()
}

function edit(t: Schedule) {
  editingTask.value = t
  showForm.value = true
}

// v0.3.3 问题 1：取消也要清空编辑目标，否则“编辑 A → 取消 → 新建”时
// 表单仍以 A 为编辑对象，保存会覆盖 A。
function closeForm() {
  showForm.value = false
  editingTask.value = null
}

function onSaved() {
  editingTask.value = null
}

async function pause(t: Schedule) {
  try {
    await tasks.pause(t.id)
    ui.toast(`任务 ${t.name} 已暂停`, 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '暂停失败', 'error')
  }
}

async function resume(t: Schedule) {
  try {
    await tasks.resume(t.id)
    ui.toast(`任务 ${t.name} 已恢复`, 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '恢复失败', 'error')
  }
}

async function remove(t: Schedule) {
  const ok = await ui.confirm({
    title: '删除定时任务',
    message: `确认删除任务 ${t.name} 吗？其运行记录将一并删除。`,
    confirmLabel: '确认删除',
    danger: true,
  })
  if (!ok) return
  try {
    await tasks.remove(t.id)
    ui.toast('定时任务已删除', 'success')
  } catch (e) {
    ui.toast(e instanceof ApiError ? e.message : '删除失败', 'error')
  }
}

function openRuns(t: Schedule) {
  void tasks.loadRuns(t)
}

// 加载失败后重试（保留当前页码）。
function retry() {
  void tasks.load()
}

function reloadRuns() {
  if (tasks.runsTask) void tasks.loadRuns(tasks.runsTask, tasks.runs?.page ?? 1)
}

onMounted(() => {
  void Promise.all([tasks.load(), devices.load(), devices.loadGroups(), devices.loadForSelector()])
})
</script>
