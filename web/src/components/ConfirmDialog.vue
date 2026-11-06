<template>
  <div
    v-if="ui.confirmState"
    class="fixed inset-0 z-[90] flex items-end justify-center bg-black/60 p-4 sm:items-center"
    @click.self="ui.resolveConfirm(false)"
  >
    <div
      role="alertdialog"
      aria-modal="true"
      :aria-label="ui.confirmState.title"
      class="card w-full max-w-sm p-5"
    >
      <h2 class="text-base font-semibold">{{ ui.confirmState.title }}</h2>
      <p class="mt-2 text-sm text-slate-600 dark:text-slate-300">{{ ui.confirmState.message }}</p>
      <div class="mt-5 flex justify-end gap-2">
        <button class="btn-secondary min-h-[44px]" @click="ui.resolveConfirm(false)">取消</button>
        <button
          class="min-h-[44px]"
          :class="ui.confirmState.danger ? 'btn-danger' : 'btn-primary'"
          @click="ui.resolveConfirm(true)"
        >
          {{ ui.confirmState.confirmLabel ?? '确认' }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useUiStore } from '../stores/ui'

const ui = useUiStore()
</script>
