import { computed, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'

export function usePolling() {
  const route = useRoute()
  const visible = ref(document.visibilityState === 'visible')
  const listener = () => { visible.value = document.visibilityState === 'visible' }
  document.addEventListener('visibilitychange', listener)
  onUnmounted(() => document.removeEventListener('visibilitychange', listener))
  return computed(() => visible.value && route.query.refresh !== 'paused' && !route.query.cursor ? 5000 : false)
}
export function queryText(value: unknown): string { return typeof value === 'string' ? value : '' }
