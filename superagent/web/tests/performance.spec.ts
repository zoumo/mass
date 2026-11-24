import { expect, test } from '@playwright/test'

test.use({ locale: 'en-US', timezoneId: 'Asia/Shanghai' })

const from = Date.UTC(2026, 10, 20) / 1000
const stepSeconds = 60
const t = (index: number) => from + index * stepSeconds
const performanceURL = '**/v1/telemetry/performance?*'
const titles = ['CPU 使用量', 'RSS 内存', 'Goroutine 数', '打开的文件描述符']
const coreInstance = 'a347907b-b5a4-4f1f-9b8a-d3995d199799'
const restartedInstance = '28e33520-d4b3-440d-af28-ef4231d31826'
const massdSeries = { dimensions: { service: 'massd', instance: 'c63c003a-c250-42e6-84a8-6a0778f3bfef' } }
const defaultMetrics = [
 {
  id: 'cpu', series: [
   { dimensions: { service: 'superagent-core', instance: coreInstance }, points: [[t(0), 0.05], [t(1), 0.12], [t(4), 0.31], [t(5), 0.4]] },
   { dimensions: { service: 'superagent-core', instance: restartedInstance }, points: [[t(6), 0.00412345]] },
   { ...massdSeries, points: [[t(0), 1.12], [t(1), 1.4], [t(2), 0.9]] },
  ],
 },
 { id: 'rss', series: [{ ...massdSeries, points: [[t(0), 67108864]] }] },
 { id: 'goroutines', series: [{ ...massdSeries, points: [[t(0), 41]] }] },
 { id: 'file_descriptors', series: [{ ...massdSeries, points: [[t(0), 16]] }] },
]

test.beforeEach(async ({ page }) => {
 await page.addInitScript(() => sessionStorage.setItem('observer-paused', 'true'))
})

test('plots four system metrics, preserves instance boundaries and restores the selected range', async ({ page }) => {
 const requests: string[] = []
 await page.route(performanceURL, async route => {
  const request = route.request()
  const range = new URL(request.url()).searchParams.get('window') || ''
  requests.push(range)
  expect(request.method()).toBe('GET')
  expect(request.headers()['authorization']).toBeUndefined()
  await route.fulfill({ json: { from, until: t(range === '6h' ? 360 : 60), stepSeconds, metrics: defaultMetrics } })
 })

 await page.goto('/superagent/metrics')
 const range = page.getByRole('combobox', { name: '时间范围', exact: true })
 await expect(range).toHaveValue('1h')
 for (const title of titles) await expect(page.getByRole('region', { name: title, exact: true })).toBeVisible()

 const cpu = page.getByRole('region', { name: titles[0], exact: true })
 await expect(cpu.getByText('08:00', { exact: true })).toBeVisible()
 await expect(cpu.getByRole('list', { name: 'CPU 使用量时序' })).toContainText(`superagent-core · ${restartedInstance}`)
 const original = cpu.locator(`g[data-instance="${coreInstance}"] path`)
 const restart = cpu.locator(`g[data-instance="${restartedInstance}"] path`)
 // Core's first instance has two segments. The new instance is a separate sampled point.
 await expect(original).toHaveAttribute('d', /^(M[^ML]+L[^ML]+){2}$/)
 await expect(restart).toHaveAttribute('d', /^M[^ML]+h0\.01$/)

 const sample = cpu.getByRole('slider', { name: 'CPU 使用量采样点' })
 const readout = cpu.getByLabel('CPU 使用量样本')
 await expect(readout.getByTitle('0.00412345 核', { exact: true })).toContainText('0.0041 核')
 await expect(readout.getByTitle(`superagent-core · ${restartedInstance}`, { exact: true })).toContainText(restartedInstance)
 await sample.focus()
 await sample.press('Home')
 await expect(sample).toHaveAttribute('aria-valuetext', /08:00:00 GMT\+8/)
 await expect(readout).toContainText('0.05 核')
 await expect(readout).toContainText('1.12 核')
 await sample.press('ArrowRight')
 await expect(readout).toContainText('0.12 核')
 await expect(readout).toContainText('1.4 核')
 await expect(page.getByLabel('RSS 内存样本')).toContainText('67,108,864 B')
 await expect(page.getByLabel('Goroutine 数样本')).toContainText('41 个')
 await expect(page.getByLabel('打开的文件描述符样本')).toContainText('16 个')

 await range.selectOption('6h')
 await expect(page).toHaveURL(/metric=process&window=6h&groupBy=instance$/)
 await expect(cpu.getByText('14:00', { exact: true })).toBeVisible()
 await page.reload()
 await expect(range).toHaveValue('6h')
 await expect(cpu.getByText('14:00', { exact: true })).toBeVisible()
 expect(requests).toEqual(['1h', '6h', '6h'])
 await page.setViewportSize({ width: 390, height: 820 })
 expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)

 await page.goBack()
 await expect(range).toHaveValue('1h')
})

test('shows loading, distinguishes missing samples from unavailable metrics, and surfaces request errors', async ({ page }) => {
 let release = () => {}
 const pending = new Promise<void>(resolve => { release = resolve })
 await page.route(performanceURL, async route => {
  if (new URL(route.request().url()).searchParams.get('window') === '24h') {
   await route.fulfill({ status: 503, json: {} })
   return
  }
  await pending
  await route.fulfill({ json: {
   from, until: t(60), stepSeconds,
   metrics: [{ id: 'cpu', series: [] }, { id: 'rss', series: [], unavailable: true }, ...defaultMetrics.slice(2)],
  } })
 })
 await page.goto('/superagent/metrics')
 await expect(page.getByRole('status')).toHaveText('正在读取指标…')
 release()
 await expect(page.getByRole('region', { name: titles[0], exact: true })).toContainText('当前范围暂无样本。')
 await expect(page.getByRole('region', { name: titles[1], exact: true }).getByRole('status')).toHaveText('该指标暂不可用。')
 await expect(page.getByRole('region', { name: titles[2], exact: true }).getByRole('slider')).toBeVisible()
 await page.getByRole('combobox', { name: '时间范围', exact: true }).selectOption('24h')
 await expect(page.getByRole('alert')).toHaveText('读取失败（HTTP 503）')
 await expect(page.getByRole('region', { name: titles[2], exact: true })).toHaveCount(0)
})


test('selects and groups MCP metrics with matching filters', async ({ page }) => {
 const requests: URLSearchParams[] = []
 await page.route(performanceURL, async route => {
  const query = new URL(route.request().url()).searchParams
  requests.push(query)
  const id = query.get('metric')
  const dimensions = query.get('groupBy') === 'tool' ? { tool: 'update_topic' } : { role: 'topic' }
  const result = id === 'process'
   ? defaultMetrics
   : [{ id, series: [{ dimensions, points: [[t(0), id === 'mcp_failure_rate' ? 0.25 : 0.5]] }] }]
  await route.fulfill({ json: { from, until: t(60), stepSeconds, metrics: result } })
 })
 await page.goto('/superagent/metrics?metric=cpu&groupBy=service&service=massd')
 await expect(page.getByRole('region', { name: 'CPU 使用量', exact: true })).toBeVisible()
 expect(Object.fromEntries(requests.at(-1)!)).toEqual({ metric: 'cpu', window: '1h', groupBy: 'service', service: 'massd' })
 await page.getByRole('textbox', { name: '服务实例' }).fill(massdSeries.dimensions.instance)
 await page.getByRole('button', { name: '应用筛选' }).click()
 await expect.poll(() => requests.at(-1)?.get('instance')).toBe(massdSeries.dimensions.instance)

 const selected = page.getByRole('combobox', { name: '指标' })
 await selected.selectOption('mcp_rate')
 await expect(page.getByRole('region', { name: 'MCP 调用率' })).toBeVisible()
 expect(Object.fromEntries(requests.at(-1)!)).toEqual({ metric: 'mcp_rate', window: '1h', groupBy: 'role' })
 await page.getByRole('combobox', { name: '分组' }).selectOption('tool')
 await page.getByRole('combobox', { name: '角色' }).selectOption('topic')
 await page.getByRole('textbox', { name: '工具' }).fill('update_topic')
 await page.getByRole('textbox', { name: '结果' }).fill('ok')
 await page.getByRole('button', { name: '应用筛选' }).click()
 await expect(page.getByRole('list', { name: 'MCP 调用率时序' })).toContainText('update_topic')
 await expect.poll(() => Object.fromEntries(requests.at(-1)!)).toEqual({
  metric: 'mcp_rate', window: '1h', groupBy: 'tool', role: 'topic', tool: 'update_topic', outcome: 'ok',
 })
 await selected.selectOption('mcp_failure_rate')
 await expect(page.getByLabel('MCP 失败率样本')).toContainText('25 %')
 await expect(page.getByRole('textbox', { name: '结果' })).toHaveCount(0)
 expect(Object.fromEntries(requests.at(-1)!)).toEqual({
  metric: 'mcp_failure_rate', window: '1h', groupBy: 'tool', role: 'topic', tool: 'update_topic',
 })
 await selected.selectOption('mcp_p95')
 await expect(page.getByLabel('MCP P95 耗时样本')).toContainText('0.5 秒')
})
