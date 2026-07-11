<script setup lang="ts">
import { computed, ref } from 'vue'
import { Check, Clipboard, Container, Link2, Rocket, Sparkles } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import PageHero from '@/components/PageHero.vue'
import { copyText } from '@/lib/utils'

const input = ref('')
const output = ref('')
const error = ref('')
const copied = ref(false)

const host = computed(() => window.location.host)

const features = [
  { icon: Rocket, label: 'GitHub 加速' },
  { icon: Container, label: 'Docker 镜像' },
  { icon: Sparkles, label: 'Hugging Face' },
] as const

const dockerExamples = computed(() => [
  {
    id: 'official',
    label: '官方镜像',
    original: 'docker pull nginx',
    accelerated: `docker pull ${host.value}/nginx`,
  },
  {
    id: 'user',
    label: '用户镜像',
    original: 'docker pull user/app:tag',
    accelerated: `docker pull ${host.value}/user/app:tag`,
  },
  {
    id: 'ghcr',
    label: 'GHCR',
    original: 'docker pull ghcr.io/org/app',
    accelerated: `docker pull ${host.value}/ghcr.io/org/app`,
  },
])

const allowedHosts = [
  'github.com/',
  'raw.githubusercontent.com/',
  'gist.githubusercontent.com/',
  'huggingface.co/',
  'cdn-lfs.hf.co/',
]

function formatLink() {
  error.value = ''
  copied.value = false
  const link = input.value.trim()
  if (!link) {
    error.value = '请输入有效的链接'
    output.value = ''
    return
  }

  if (link.startsWith('https://') || link.startsWith('http://')) {
    output.value = `https://${host.value}/${link}`
    return
  }

  if (allowedHosts.some((prefix) => link.startsWith(prefix))) {
    output.value = `https://${host.value}/https://${link}`
    return
  }

  error.value = '请输入有效的 GitHub / Hugging Face 链接'
  output.value = ''
}

async function onCopy() {
  if (!output.value) return
  copied.value = await copyText(output.value)
}

function onOpen() {
  if (!output.value) return
  window.open(output.value, '_blank', 'noopener,noreferrer')
}
</script>

<template>
  <div class="mx-auto max-w-3xl">
    <PageHero
      eyebrow="面向开发者和运维人员的加速服务"
      title="HubProxy"
      subtitle="GitHub 文件加速 · Docker 镜像加速 · Hugging Face 资源"
      gradient
    >
      <div class="flex flex-wrap justify-center gap-2 pt-2">
        <span
          v-for="item in features"
          :key="item.label"
          class="feature-pill"
        >
          <component :is="item.icon" class="size-4" />
          {{ item.label }}
        </span>
      </div>
    </PageHero>

    <section class="surface-panel field-block">
      <div class="flex flex-col gap-3 sm:flex-row">
        <Input
          v-model="input"
          class="sm:flex-1"
          placeholder="粘贴 GitHub / Hugging Face 原始链接"
          @keyup.enter="formatLink"
        />
        <Button @click="formatLink">获取加速链接</Button>
      </div>

      <Transition name="fade" mode="out-in">
        <p v-if="error" key="error" class="text-center text-destructive">{{ error }}</p>
        <div v-else-if="output" key="output" class="space-y-4 pt-2">
          <div class="flex items-center justify-center gap-2 font-medium text-primary">
            <Check class="size-4" />
            加速链接已生成
          </div>
          <p class="break-all rounded-lg border border-border bg-muted/40 px-4 py-3.5 font-mono">
            {{ output }}
          </p>
          <div class="flex flex-wrap justify-center gap-2">
            <Button variant="secondary" size="sm" @click="onCopy">
              <Clipboard class="size-4" />
              {{ copied ? '已复制' : '复制链接' }}
            </Button>
            <Button variant="secondary" size="sm" @click="onOpen">
              <Link2 class="size-4" />
              打开链接
            </Button>
          </div>
        </div>
      </Transition>
    </section>

    <section class="space-y-6 pt-12">
      <div class="space-y-1 text-center">
        <h2 class="text-sm font-semibold tracking-[0.16em] text-muted-foreground uppercase">
          Docker 镜像加速
        </h2>
        <p class="text-muted-foreground">
          在镜像名前加上本站域名，一行命令即可加速拉取
        </p>
      </div>

      <div class="terminal-block">
        <div class="terminal-header">
          <span class="terminal-dot" />
          <span class="terminal-dot" />
          <span class="terminal-dot" />
          <span class="ml-2 text-xs text-muted-foreground">shell</span>
        </div>
        <div class="terminal-body">
          <div
            v-for="item in dockerExamples"
            :key="item.id"
            class="terminal-example"
          >
            <span class="example-tag">{{ item.label }}</span>
            <p class="font-mono leading-relaxed">
              <span class="text-muted-foreground">$ </span>
              <span class="text-muted-foreground/70 line-through decoration-muted-foreground/40">{{ item.original }}</span>
            </p>
            <p class="font-mono leading-relaxed">
              <span class="text-muted-foreground">$ </span>
              <span class="text-primary">{{ item.accelerated }}</span>
            </p>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>
