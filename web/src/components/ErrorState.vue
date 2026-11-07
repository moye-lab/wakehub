<!-- 统一的加载失败状态（v0.2.9 问题 16）：接口失败不白屏、不用假数据。
     完整版居中展示（列表为空时替代空状态）；compact 版为一行横条
     （列表已有旧数据时置顶提示，不遮住旧数据）。 -->
<template>
  <div v-if="compact" class="flex flex-wrap items-center justify-between gap-3 rounded-lg bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:bg-rose-950 dark:text-rose-300">
    <span class="min-w-0">⚠️ 加载失败：{{ message }}</span>
    <button class="btn-secondary min-h-[40px] shrink-0 px-3" @click="$emit('retry')">重试</button>
  </div>
  <div v-else class="flex flex-col items-center justify-center px-4 py-10 text-center">
    <div class="text-3xl" aria-hidden="true">⚠️</div>
    <p class="mt-2 text-sm font-medium text-slate-700 dark:text-slate-200">加载失败</p>
    <p class="mt-1 max-w-sm text-xs text-slate-500 dark:text-slate-400">{{ message }}</p>
    <button class="btn-secondary mt-4 min-h-[44px]" @click="$emit('retry')">重试</button>
  </div>
</template>

<script setup lang="ts">
withDefaults(defineProps<{ message: string; compact?: boolean }>(), { compact: false })
defineEmits<{ (e: 'retry'): void }>()
</script>
