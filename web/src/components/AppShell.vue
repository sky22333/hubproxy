<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { Container, Github, Menu, Rocket, Search, X, Zap } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import ThemeToggle from '@/components/ThemeToggle.vue'

const STORAGE_KEY = 'theme'
const route = useRoute()
const isDark = ref(false)
const menuOpen = ref(false)

const links = [
  { to: '/', label: 'GitHub 加速', icon: Rocket },
  { to: '/images', label: '离线镜像', icon: Container },
  { to: '/search', label: '镜像搜索', icon: Search },
] as const

const currentPath = computed(() => route.path)

function applyTheme(dark: boolean) {
  isDark.value = dark
  document.documentElement.classList.toggle('dark', dark)
  localStorage.setItem(STORAGE_KEY, dark ? 'dark' : 'light')
}

function toggleTheme() {
  applyTheme(!isDark.value)
}

function closeMenu() {
  menuOpen.value = false
}

onMounted(() => {
  const saved = localStorage.getItem(STORAGE_KEY)
  if (saved === 'dark' || saved === 'light') {
    applyTheme(saved === 'dark')
  } else {
    applyTheme(window.matchMedia('(prefers-color-scheme: dark)').matches)
  }
})
</script>

<template>
  <div class="shell-atmosphere flex min-h-screen flex-col text-foreground">
    <header class="sticky top-0 z-50 border-b border-border/50 bg-background/70 backdrop-blur-xl">
      <div class="mx-auto flex h-[4.25rem] max-w-6xl items-center justify-between gap-3 px-5 sm:px-8">
        <RouterLink
          to="/"
          class="flex items-center gap-3 font-display text-lg font-semibold tracking-tight transition-opacity hover:opacity-80"
          @click="closeMenu"
        >
          <span class="brand-mark flex size-9 items-center justify-center rounded-lg">
            <Zap class="size-[18px]" />
          </span>
          <span>HubProxy</span>
        </RouterLink>

        <nav class="hidden items-center gap-1.5 md:flex">
          <RouterLink
            v-for="link in links"
            :key="link.to"
            :to="link.to"
            class="inline-flex items-center gap-1.5 rounded-full px-4 py-2 text-[15px] transition-colors duration-150"
            :class="currentPath === link.to ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-accent hover:text-foreground'"
          >
            <component :is="link.icon" class="size-4" />
            {{ link.label }}
          </RouterLink>
          <ThemeToggle :is-dark="isDark" button-class="ml-1" @toggle="toggleTheme" />
        </nav>

        <div class="flex items-center gap-0.5 md:hidden">
          <ThemeToggle :is-dark="isDark" @toggle="toggleTheme" />
          <Button variant="ghost" size="icon" aria-label="菜单" @click="menuOpen = !menuOpen">
            <Transition name="fade" mode="out-in">
              <X v-if="menuOpen" key="x" class="size-4" />
              <Menu v-else key="menu" class="size-4" />
            </Transition>
          </Button>
        </div>
      </div>

      <Transition name="menu">
        <div v-if="menuOpen" class="border-t border-border px-5 py-2 md:hidden">
          <div class="flex flex-col gap-1">
            <RouterLink
              v-for="link in links"
              :key="link.to"
              :to="link.to"
              class="inline-flex items-center gap-2 rounded-full px-3.5 py-2 text-[15px] transition-colors"
              :class="currentPath === link.to ? 'bg-primary text-primary-foreground' : 'text-muted-foreground'"
              @click="closeMenu"
            >
              <component :is="link.icon" class="size-4" />
              {{ link.label }}
            </RouterLink>
          </div>
        </div>
      </Transition>
    </header>

    <main class="mx-auto w-full max-w-6xl flex-1 px-5 py-10 text-base sm:px-8 sm:py-16">
      <slot />
    </main>

    <footer class="flex justify-center pb-10 pt-2">
      <a
        href="https://github.com/sky22333/hubproxy"
        target="_blank"
        rel="noopener noreferrer"
        aria-label="GitHub"
        class="text-muted-foreground transition-colors duration-150 hover:text-foreground"
      >
        <Github class="size-5" />
      </a>
    </footer>
  </div>
</template>
