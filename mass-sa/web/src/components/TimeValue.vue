<script setup lang="ts">
defineProps<{ value?: string; compact?: boolean }>()
function format(value: string) {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'long' }).format(new Date(value))
}
function time(value: string) {
  return new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(value))
}
function date(value: string) {
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit' }).format(new Date(value))
}
</script>
<template><time v-if="value && !value.startsWith('0001-')" :datetime="value" :title="`${format(value)} · ${value}`" :class="{ 'compact-time': compact }"><template v-if="compact"><span>{{ time(value) }}</span><small>{{ date(value) }}</small></template><template v-else>{{ format(value) }}</template></time><span v-else>未观测</span></template>
<style scoped>
.compact-time { display: flex; flex-direction: column; gap: 5px; white-space: nowrap; }
.compact-time small { color: var(--muted); font-size: 11px; }
</style>
