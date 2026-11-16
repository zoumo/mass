import { nextTick, ref, watch, type ComputedRef } from 'vue'

// Only an explicit row selection moves focus. Polling, URL reload and browser
// back leave the reading position under the page's existing restoration rules.
export function useDetailNavigation(identity: ComputedRef<string>, headingSelector: string) {
  const requested = ref('')
  let rowId = ''
  function requestDetail(key: string, row: string) {
    if (window.matchMedia('(max-width: 70rem)').matches) {
      rowId = row
      requested.value = key
    }
  }
  watch([requested, identity], async ([wanted, current]) => {
    if (!wanted || wanted !== current) return
    await nextTick()
    const heading = document.querySelector<HTMLElement>(headingSelector)
    if (heading) {
      heading.focus({ preventScroll: true })
      heading.scrollIntoView({ block: 'start' })
      requested.value = ''
    }
  }, { flush: 'post' })
  function returnToRow(fallback: string) {
    const row = document.getElementById(rowId || fallback)
    row?.focus({ preventScroll: true })
    row?.scrollIntoView({ block: 'center' })
  }
  return { requestDetail, returnToRow }
}
