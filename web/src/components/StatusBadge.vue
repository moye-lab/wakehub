<template>
  <span
    class="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium"
    :class="toneClass"
  >
    <span v-if="dot" class="inline-block h-1.5 w-1.5 rounded-full" :class="dotClass" />
    {{ label }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{ tone: 'green' | 'red' | 'amber' | 'blue' | 'gray'; label: string; dot?: boolean }>(),
  { dot: true },
)

const toneClass = computed(() => {
  switch (props.tone) {
    case 'green':
      return 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-800 dark:bg-emerald-950 dark:text-emerald-400'
    case 'red':
      return 'border-rose-200 bg-rose-50 text-rose-700 dark:border-rose-800 dark:bg-rose-950 dark:text-rose-400'
    case 'amber':
      return 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-400'
    case 'blue':
      return 'border-blue-200 bg-blue-50 text-blue-700 dark:border-blue-800 dark:bg-blue-950 dark:text-blue-400'
    default:
      return 'border-slate-200 bg-slate-50 text-slate-600 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300'
  }
})

const dotClass = computed(() => {
  switch (props.tone) {
    case 'green':
      return 'bg-emerald-500'
    case 'red':
      return 'bg-rose-500'
    case 'amber':
      return 'bg-amber-500'
    case 'blue':
      return 'bg-blue-500'
    default:
      return 'bg-slate-400'
  }
})
</script>
