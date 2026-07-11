<script setup lang="ts">
defineProps<{
  eyebrow: string
  title: string
  subtitle: string
  gradient?: boolean
}>()
</script>

<template>
  <header class="page-hero">
    <p class="eyebrow">{{ eyebrow }}</p>
    <h1 class="display-title" :class="{ 'gradient-text': gradient }">{{ title }}</h1>
    <p class="mx-auto max-w-xl text-lg text-muted-foreground sm:text-xl">
      {{ subtitle }}
    </p>
    <slot />
  </header>
</template>
