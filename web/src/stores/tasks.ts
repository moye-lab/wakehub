// 定时任务 store：列表（分页）、创建/更新/删除、暂停/恢复、运行记录。
import { defineStore } from 'pinia'
import { reactive, ref } from 'vue'
import { api } from '../api'
import { ApiError, type Page, type Schedule, type TaskRun } from '../api/types'

export const useTasksStore = defineStore('tasks', () => {
  const items = ref<Schedule[]>([])
  const total = ref(0)
  const page = ref(1)
  const pageSize = ref(50)
  const loading = ref(false)
  // 列表 / 运行记录加载失败文案（v0.2.9 问题 16），见 devices.ts。
  const error = ref('')
  const runsError = ref('')
  const busyIds = reactive(new Set<string>())
  /** 某任务的运行记录（抽屉展示用）。 */
  const runs = ref<Page<TaskRun> | null>(null)
  const runsLoading = ref(false)
  const runsTask = ref<Schedule | null>(null)

  async function load() {
    loading.value = true
    error.value = ''
    try {
      const data = await api.listTasks(page.value, pageSize.value)
      items.value = data.items
      total.value = data.total
      page.value = data.page
    } catch (e) {
      error.value = e instanceof ApiError ? e.message : '加载定时任务失败'
    } finally {
      loading.value = false
    }
  }

  async function create(body: Record<string, unknown>) {
    const task = await api.createTask(body)
    items.value.unshift(task)
    total.value += 1
    return task
  }

  async function update(id: string, body: Record<string, unknown>) {
    const task = await api.updateTask(id, body)
    const idx = items.value.findIndex((t) => t.id === id)
    if (idx >= 0) items.value[idx] = task
    return task
  }

  async function remove(id: string) {
    await api.deleteTask(id)
    items.value = items.value.filter((t) => t.id !== id)
    total.value = Math.max(0, total.value - 1)
  }

  async function pause(id: string) {
    busyIds.add(id)
    try {
      const task = await api.pauseTask(id)
      const idx = items.value.findIndex((t) => t.id === id)
      if (idx >= 0) items.value[idx] = task
    } finally {
      busyIds.delete(id)
    }
  }

  async function resume(id: string) {
    busyIds.add(id)
    try {
      const task = await api.resumeTask(id)
      const idx = items.value.findIndex((t) => t.id === id)
      if (idx >= 0) items.value[idx] = task
    } finally {
      busyIds.delete(id)
    }
  }

  async function loadRuns(task: Schedule, p = 1) {
    runsTask.value = task
    runsLoading.value = true
    runsError.value = ''
    try {
      runs.value = await api.listTaskRuns(task.id, p, 50)
    } catch (e) {
      runsError.value = e instanceof ApiError ? e.message : '加载运行记录失败'
    } finally {
      runsLoading.value = false
    }
  }

  function closeRuns() {
    runs.value = null
    runsTask.value = null
  }

  return {
    items, total, page, pageSize, loading, error, busyIds,
    runs, runsLoading, runsError, runsTask,
    load, create, update, remove, pause, resume, loadRuns, closeRuns,
  }
})
