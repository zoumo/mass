const decimal = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 })
export function elapsed(ns: number) {
  if (ns >= 1e9) return `${decimal.format(ns / 1e9)} s`
  if (ns >= 1e6 || ns === 0) return `${decimal.format(ns / 1e6)} ms`
  return `${decimal.format(ns / 1e3)} µs`
}
