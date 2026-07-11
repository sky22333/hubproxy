<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { cn } from '@/lib/utils'

const props = withDefaults(
  defineProps<{
    variant?: 'default' | 'secondary' | 'outline' | 'ghost'
    size?: 'default' | 'sm' | 'icon'
    disabled?: boolean
    class?: HTMLAttributes['class']
  }>(),
  {
    variant: 'default',
    size: 'default',
  },
)

const variants: Record<NonNullable<typeof props.variant>, string> = {
  default: 'bg-primary text-primary-foreground hover:bg-primary/90',
  secondary: 'bg-secondary text-secondary-foreground hover:bg-secondary/80',
  outline: 'border border-input bg-transparent hover:bg-accent hover:text-accent-foreground',
  ghost: 'hover:bg-accent hover:text-accent-foreground text-muted-foreground',
}

const sizes: Record<NonNullable<typeof props.size>, string> = {
  default: 'h-11 px-5 text-base',
  sm: 'h-9 px-3 text-sm',
  icon: 'size-11',
}
</script>

<template>
  <button
    type="button"
    :disabled="disabled"
    :class="cn(
      'inline-flex items-center justify-center gap-1.5 rounded-lg font-medium outline-none transition-[opacity,transform,background-color,color] duration-150 ease-out active:scale-[0.98] focus-visible:ring-2 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50',
      variants[variant],
      sizes[size],
      props.class,
    )"
  >
    <slot />
  </button>
</template>
