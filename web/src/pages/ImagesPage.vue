<script setup lang="ts">
import { ref } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import {
  fetchImageInfo,
  prepareBatchDownload,
  prepareSingleDownload,
  triggerDownload,
} from '@/api'
import { errorMessage } from '@/lib/utils'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import PageHero from '@/components/PageHero.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'

const singleImage = ref('')
const singlePlatform = ref('linux/amd64')
const singleCompressed = ref(true)
const singleStatus = ref('')
const singleError = ref('')
const singleLoading = ref(false)

const batchText = ref('')
const batchPlatform = ref('linux/amd64')
const batchCompressed = ref(true)
const batchStatus = ref('')
const batchError = ref('')
const batchLoading = ref(false)

async function preflight(images: string[]) {
  for (const image of [...new Set(images)]) {
    await fetchImageInfo(image)
  }
}

async function onSingleSubmit() {
  singleError.value = ''
  singleStatus.value = ''
  const image = singleImage.value.trim()
  if (!image) {
    singleError.value = '请输入镜像名称'
    return
  }

  singleLoading.value = true
  singleStatus.value = '正在准备下载...'
  try {
    await preflight([image])
    const data = await prepareSingleDownload({
      image,
      platform: singlePlatform.value,
      compressed: singleCompressed.value,
    })
    if (!data.download_url) throw new Error('下载地址生成失败')
    triggerDownload(data.download_url)
    const platformText = singlePlatform.value.trim()
      ? ` (${singlePlatform.value.trim()})`
      : ''
    singleStatus.value = `开始下载 ${image}${platformText}`
  } catch (e) {
    singleStatus.value = ''
    singleError.value = errorMessage(e, '下载失败')
  } finally {
    singleLoading.value = false
  }
}

async function onBatchSubmit() {
  batchError.value = ''
  batchStatus.value = ''
  const images = batchText.value
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith('#'))

  if (images.length === 0) {
    batchError.value = '请输入镜像列表'
    return
  }

  batchLoading.value = true
  batchStatus.value = '正在准备批量下载...'
  try {
    await preflight(images)
    const data = await prepareBatchDownload({
      images,
      platform: batchPlatform.value,
      useCompressedLayers: batchCompressed.value,
    })
    if (!data.download_url) throw new Error('下载地址生成失败')
    triggerDownload(data.download_url)
    batchStatus.value = `开始下载 ${images.length} 个镜像`
  } catch (e) {
    batchStatus.value = ''
    batchError.value = errorMessage(e, '下载失败')
  } finally {
    batchLoading.value = false
  }
}
</script>

<template>
  <div class="mx-auto max-w-3xl">
    <PageHero
      eyebrow="Offline Image"
      title="离线镜像"
      subtitle="流式下载，兼容 docker load，支持多架构。"
    />

    <section class="field-block">
      <h2 class="text-center text-sm font-semibold tracking-[0.16em] text-muted-foreground uppercase">
        单镜像
      </h2>

      <Transition name="fade" mode="out-in">
        <p v-if="singleError" key="error" class="text-center text-destructive">{{ singleError }}</p>
        <p v-else-if="singleStatus" key="status" class="flex items-center justify-center gap-2 text-muted-foreground">
          <Loader2 v-if="singleLoading" class="size-4 animate-spin" />
          {{ singleStatus }}
        </p>
      </Transition>

      <label class="block space-y-1.5">
        <span>镜像名称</span>
        <Input v-model="singleImage" placeholder="nginx 或 user/app:tag" />
      </label>
      <label class="block space-y-1.5">
        <span>目标架构（可选）</span>
        <Input v-model="singlePlatform" placeholder="linux/amd64" />
      </label>
      <div class="flex items-center justify-between py-1">
        <span>压缩层</span>
        <Switch v-model:checked="singleCompressed" />
      </div>
      <Button class="w-full" :disabled="singleLoading" @click="onSingleSubmit">
        <Loader2 v-if="singleLoading" class="size-4 animate-spin" />
        {{ singleLoading ? '准备中...' : '立即下载' }}
      </Button>
    </section>

    <section class="section-gap field-block">
      <h2 class="text-center text-sm font-semibold tracking-[0.16em] text-muted-foreground uppercase">
        批量下载
      </h2>

      <Transition name="fade" mode="out-in">
        <p v-if="batchError" key="error" class="text-center text-destructive">{{ batchError }}</p>
        <p v-else-if="batchStatus" key="status" class="flex items-center justify-center gap-2 text-muted-foreground">
          <Loader2 v-if="batchLoading" class="size-4 animate-spin" />
          {{ batchStatus }}
        </p>
      </Transition>

      <label class="block space-y-1.5">
        <span>镜像列表</span>
        <Textarea
          v-model="batchText"
          placeholder="alpine&#10;redis:alpine&#10;user/app:1.0"
        />
      </label>
      <label class="block space-y-1.5">
        <span>目标架构（可选）</span>
        <Input v-model="batchPlatform" placeholder="linux/amd64" />
      </label>
      <div class="flex items-center justify-between py-1">
        <span>压缩层</span>
        <Switch v-model:checked="batchCompressed" />
      </div>
      <Button class="w-full" :disabled="batchLoading" @click="onBatchSubmit">
        <Loader2 v-if="batchLoading" class="size-4 animate-spin" />
        {{ batchLoading ? '准备中...' : '批量下载' }}
      </Button>
    </section>
  </div>
</template>
