<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-xl font-bold">操作日志</h1>
      <button class="btn-secondary min-h-[44px]" :disabled="logs.loading" @click="load">刷新</button>
    </div>

    <!-- 筛选 -->
    <div class="card flex flex-wrap items-end gap-3 p-4">
      <label class="field min-w-44 flex-1">
        <span>操作类型</span>
        <input v-model="logs.filters.action" class="input font-mono" list="log-actions" placeholder="如 device_wake，多个用逗号分隔" />
        <datalist id="log-actions">
          <option v-for="a in KNOWN_ACTIONS" :key="a" :value="a" />
        </datalist>
      </label>
      <label class="field">
        <span>结果</span>
        <select v-model="logs.filters.result" class="input">
          <option value="">全部</option>
          <option value="success">成功</option>
          <option value="failed">失败</option>
        </select>
      </label>
      <label class="field">
        <span>操作者</span>
        <input v-model="logs.filters.actor" class="input" placeholder="如 admin" />
      </label>
      <label class="field">
        <span>目标类型</span>
        <select v-model="logs.filters.target_type" class="input">
          <option value="">全部</option>
          <option value="device">设备</option>
          <option value="group">分组</option>
          <option value="agent">Agent</option>
          <option value="schedule">定时任务</option>
          <option value="subnet">网段</option>
          <option value="user">用户</option>
        </select>
      </label>
      <label class="field">
        <span>开始时间</span>
        <input v-model="fromInput" class="input" type="datetime-local" />
      </label>
      <label class="field">
        <span>结束时间</span>
        <input v-model="toInput" class="input" type="datetime-local" />
      </label>
      <button class="btn-primary min-h-[44px]" @click="applyFilters">筛选</button>
      <button class="btn-secondary min-h-[44px]" @click="reset">重置</button>
    </div>

    <ErrorState v-if="logs.error && logs.items.length > 0" :message="logs.error" compact @retry="load" />
    <div class="card overflow-hidden">
      <div v-if="logs.loading" class="p-8 text-center text-sm text-slate-500">加载中…</div>
      <ErrorState v-else-if="logs.error" :message="logs.error" @retry="load" />
      <EmptyState
        v-else-if="logs.items.length === 0"
        title="暂无日志"
        hint="调整筛选条件或稍后再试。审计日志默认保留 90 天。"
        icon="📋"
      />
      <ul v-else class="divide-y divide-slate-100 dark:divide-slate-800">
        <li v-for="l in logs.items" :key="l.id" class="px-4 py-3">
          <button class="flex w-full flex-wrap items-start justify-between gap-2 text-left" @click="toggleExpand(l.id)">
            <span class="min-w-0">
              <span class="mr-2 rounded bg-slate-100 px-2 py-0.5 font-mono text-xs text-blue-700 dark:bg-slate-800 dark:text-blue-300">{{ l.action }}</span>
              <StatusBadge :tone="l.result === 'success' ? 'green' : 'red'" :label="l.result === 'success' ? '成功' : '失败'" />
              <span class="ml-2 break-words text-sm">{{ l.detail }}</span>
            </span>
            <span class="shrink-0 text-xs text-slate-500 dark:text-slate-400">{{ formatTime(l.created_at) }}</span>
          </button>
          <div v-if="expanded.has(l.id)" class="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 text-xs text-slate-500 dark:text-slate-400 sm:grid-cols-4">
            <span>操作者：{{ l.actor }}</span>
            <span>IP：{{ l.ip_address }}</span>
            <span>目标：{{ l.target_type }} / {{ l.target_id || '—' }}</span>
            <span>ID：{{ l.id }}</span>
          </div>
        </li>
      </ul>
      <div class="border-t border-slate-200 p-4 dark:border-slate-800">
        <Pagination
          :total="logs.total"
          :page="logs.page"
          :page-size="logs.pageSize"
          @change="changePage"
          @size-change="changeSize"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import EmptyState from '../components/EmptyState.vue'
import ErrorState from '../components/ErrorState.vue'
import Pagination from '../components/Pagination.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { KNOWN_ACTIONS, useLogsStore } from '../stores/logs'
import { formatTime, localInputToRFC3339 } from '../utils/format'

const logs = useLogsStore()
const fromInput = ref('')
const toInput = ref('')
const expanded = ref<Set<string>>(new Set())

function toggleExpand(id: string) {
  if (expanded.value.has(id)) expanded.value.delete(id)
  else expanded.value.add(id)
}

function load() {
  void logs.load()
}

function applyFilters() {
  logs.filters.from = fromInput.value ? localInputToRFC3339(fromInput.value) : ''
  logs.filters.to = toInput.value ? localInputToRFC3339(toInput.value) : ''
  logs.page = 1
  load()
}

function reset() {
  fromInput.value = ''
  toInput.value = ''
  logs.resetFilters()
  load()
}

function changePage(p: number) {
  logs.page = p
  load()
}

function changeSize(size: number) {
  logs.pageSize = size
  logs.page = 1
  load()
}

onMounted(() => {
  load()
})
</script>
