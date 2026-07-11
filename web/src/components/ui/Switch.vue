<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { cn } from '@/lib/utils'

const checked = defineModel<boolean>('checked', { default: false })

const props = defineProps<{
  id?: string
  class?: HTMLAttributes['class']
  disabled?: boolean
}>()

function toggle() {
  if (props.disabled) return
  checked.value = !checked.value
}
</script>

<template>
  <button
    :id="id"
    type="button"
    role="switch"
    :aria-checked="checked"
    :disabled="disabled"
    :class="cn(
      'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-transparent transition-colors duration-150 outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-50',
      checked ? 'bg-primary' : 'bg-muted',
      props.class,
    )"
    @click="toggle"
  >
    <span
      :class="cn(
        'pointer-events-none block size-4 rounded-full bg-background shadow-sm transition-transform duration-150 ease-out',
        checked ? 'translate-x-[16px]' : 'translate-x-0.5',
      )"
    />
  </button>
</template>
