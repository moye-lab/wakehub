<template>
  <div class="flex flex-col items-center justify-center py-10 text-center">
    <div class="text-3xl" aria-hidden="true">{{ icon }}</div>
    <p class="mt-2 text-sm font-medium text-slate-700 dark:text-slate-200">{{ title }}</p>
    <p v-if="hint" class="mt-1 max-w-sm text-xs text-slate-500 dark:text-slate-400">{{ hint }}</p>
    <slot />
  </div>
</template>

<script setup lang="ts">
withDefaults(defineProps<{ title: string; hint?: string; icon?: string }>(), { icon: '📭' })
</script>
