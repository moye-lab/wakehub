<template>
  <div v-if="total > 0" class="flex flex-wrap items-center justify-between gap-2 text-sm">
    <span class="text-slate-500 dark:text-slate-400">
      共 {{ total }} 条 · 第 {{ page }} / {{ totalPages }} 页
    </span>
    <div class="flex items-center gap-1">
      <button class="btn-ghost min-h-[44px] min-w-[44px]" :disabled="page <= 1" @click="$emit('change', 1)">«</button>
      <button class="btn-ghost min-h-[44px] min-w-[44px]" :disabled="page <= 1" @click="$emit('change', page - 1)">‹</button>
      <span class="px-2 text-slate-600 dark:text-slate-300">{{ page }} / {{ totalPages }}</span>
      <button class="btn-ghost min-h-[44px] min-w-[44px]" :disabled="page >= totalPages" @click="$emit('change', page + 1)">›</button>
      <button class="btn-ghost min-h-[44px] min-w-[44px]" :disabled="page >= totalPages" @click="$emit('change', totalPages)">»</button>
      <select
        :value="pageSize"
        class="input ml-2 w-auto min-h-[44px]"
        aria-label="每页条数"
        @change="$emit('size-change', Number(($event.target as HTMLSelectElement).value))"
      >
        <option v-for="n in [20, 50, 100, 200]" :key="n" :value="n">{{ n }} / 页</option>
      </select>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ total: number; page: number; pageSize: number }>()
defineEmits<{ (e: 'change', page: number): void; (e: 'size-change', size: number): void }>()

const totalPages = computed(() => Math.max(1, Math.ceil(props.total / props.pageSize)))
</script>
