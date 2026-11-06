<template>
  <div class="pointer-events-none fixed inset-x-0 top-4 z-[100] flex flex-col items-center gap-2 px-4">
    <div
      v-for="t in ui.toasts"
      :key="t.id"
      role="status"
      class="pointer-events-auto flex max-w-md items-center gap-2 rounded-lg px-4 py-3 text-sm font-medium text-white shadow-lg"
      :class="{
        'bg-emerald-600': t.kind === 'success',
        'bg-rose-600': t.kind === 'error',
        'bg-blue-600': t.kind === 'info',
      }"
    >
      <span aria-hidden="true">{{ t.kind === 'success' ? '✓' : t.kind === 'error' ? '✕' : 'ℹ' }}</span>
      <span class="break-words">{{ t.message }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useUiStore } from '../stores/ui'

const ui = useUiStore()
</script>
