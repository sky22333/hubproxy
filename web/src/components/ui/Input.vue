<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { cn } from '@/lib/utils'

const model = defineModel<string>({ default: '' })

const props = defineProps<{
  class?: HTMLAttributes['class']
  type?: string
  id?: string
  placeholder?: string
  disabled?: boolean
}>()
</script>

<template>
  <input
    :id="id"
    v-model="model"
    :type="type || 'text'"
    :placeholder="placeholder"
    :disabled="disabled"
    :class="cn(
      'h-11 w-full rounded-lg border border-input bg-transparent px-3.5 text-base outline-none transition-[border-color,box-shadow] duration-150 placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40 disabled:opacity-50',
      props.class,
    )"
  >
</template>
