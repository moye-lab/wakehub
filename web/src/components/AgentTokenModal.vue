<template>
  <div class="fixed inset-0 z-[80] flex items-end justify-center bg-black/60 sm:items-center sm:p-4" @click.self="close">
    <div role="dialog" aria-modal="true" class="card w-full max-w-md p-5">
      <h2 class="text-lg font-semibold">Agent Token（仅显示一次）</h2>
      <p class="mt-2 text-sm text-slate-600 dark:text-slate-300">
        请立即复制并写入 Agent 的 <code class="font-mono">WAKEHUB_AGENT_TOKEN</code> 环境变量。
        服务端只存哈希，<strong>关闭后无法再次查看</strong>；丢失后须重新签发。
      </p>
      <div class="mt-4 flex items-center gap-2">
        <code class="min-w-0 flex-1 break-all rounded-lg bg-slate-100 px-3 py-2 font-mono text-xs dark:bg-slate-800">
          {{ token }}
        </code>
        <button class="btn-secondary min-h-[44px] shrink-0" @click="copy">复制</button>
      </div>
      <div class="mt-5 flex justify-end">
        <button class="btn-primary min-h-[44px]" @click="close">我已保存</button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useUiStore } from '../stores/ui'

const props = defineProps<{ token: string }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const ui = useUiStore()

async function copy() {
  try {
    await navigator.clipboard.writeText(props.token)
    ui.toast('Token 已复制', 'success')
  } catch {
    ui.toast('复制失败，请手动选中复制', 'error')
  }
}

function close() {
  emit('close')
}
</script>
