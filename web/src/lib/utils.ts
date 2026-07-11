import type { ClassValue } from 'clsx'
import { clsx } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export function formatNumber(num: number): string {
  if (num >= 1_000_000_000) return `${(num / 1_000_000_000).toFixed(1)}B+`
  if (num >= 1_000_000) return `${(num / 1_000_000).toFixed(1)}M+`
  if (num >= 1_000) return `${(num / 1_000).toFixed(1)}K+`
  return String(num)
}

export function formatSize(bytes?: number): string {
  if (bytes == null || bytes <= 0) return ''
  const units = ['B', 'KB', 'MB', 'GB']
  let size = bytes
  let i = 0
  while (size >= 1024 && i < units.length - 1) {
    size /= 1024
    i++
  }
  return `${size.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

export function formatArchs(
  images?: Array<{ architecture?: string; os?: string; variant?: string }>,
): string[] {
  if (!images?.length) return []
  const seen = new Set<string>()
  const out: string[] = []
  for (const img of images) {
    const arch = img.architecture?.trim()
    if (!arch || arch === 'unknown') continue
    const os = img.os?.trim()
    let label = os && os !== 'unknown' ? `${os}/${arch}` : arch
    if (img.variant) label += `/${img.variant}`
    if (seen.has(label)) continue
    seen.add(label)
    out.push(label)
  }
  return out
}

export function formatTimeAgo(dateString?: string): string {
  if (!dateString) return '未知时间'
  const date = new Date(dateString)
  if (Number.isNaN(date.getTime())) return '未知时间'

  const diffMs = Math.abs(Date.now() - date.getTime())
  const minutes = Math.floor(diffMs / 60_000)
  const hours = Math.floor(diffMs / 3_600_000)
  const days = Math.floor(diffMs / 86_400_000)
  const months = Math.floor(days / 30)
  const years = Math.floor(days / 365)

  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes}分钟前`
  if (hours < 24) return `${hours}小时前`
  if (days < 7) return `${days}天前`
  if (days < 30) return `${Math.floor(days / 7)}周前`
  if (months < 12) return `${months}个月前`
  if (years < 1) return '近1年'
  return `${years}年前`
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

export function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}
