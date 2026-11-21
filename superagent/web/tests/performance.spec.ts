import { expect, test } from '@playwright/test'

test.use({ locale: 'en-US', timezoneId: 'Asia/Shanghai' })

const from = Date.UTC(2026, 10, 20) / 1000
const stepSeconds = 60
const t = (index: number) => from + index * stepSeconds
const performanceURL = '**/v1/observer/performance?*'
const titles = ['CPU 使用量', 'RSS 内存', 'Goroutine 数', '打开的文件描述符']
const coreInstance = 'a347907b-b5a4-4f1f-9b8a-d3995d199799'
const restartedInstance = '28e33520-d4b3-440d-af28-ef4231d31826'
const massdSeries = { service: 'massd', instance: 'c63c003a-c250-42e6-84a8-6a0778f3bfef' }
const defaultMetrics = [
 {
  id: 'cpu', series: [
   { service: 'superagent-core', instance: coreInstance, points: [[t(0), 0.05], [t(1), 0.12], [t(4), 0.31], [t(5), 0.4]] },
   { service: 'superagent-core', instance: restartedInstance, points: [[t(6), 0.00412345]] },
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

 await page.goto('/observer/?perspective=metrics&scope=topic&window=7d')
 const dimensions = page.getByRole('navigation', { name: 'Metrics 统计维度' })
 await expect(dimensions.getByRole('link', { name: 'Topic', exact: true })).toHaveAttribute('aria-current', 'page')
 await expect(page.getByRole('combobox', { name: '时间范围', exact: true })).toHaveValue('7d')
 await page.getByRole('link', { name: '系统性能' }).click()
 await expect(page).toHaveURL(/perspective=metrics&tab=performance&window=1h$/)
 await expect(dimensions).toHaveCount(0)
 const range = page.getByRole('combobox', { name: '性能时间范围', exact: true })
 await expect(range).toHaveValue('1h')
 for (const title of titles) await expect(page.getByRole('region', { name: title, exact: true })).toBeVisible()

 const cpu = page.getByRole('region', { name: titles[0], exact: true })
 await expect(cpu.getByText('08:00', { exact: true })).toBeVisible()
 await expect(cpu.getByRole('list', { name: 'CPU 使用量服务与实例' })).toContainText(`superagent-core ${restartedInstance}`)
 const original = cpu.locator(`g[data-instance="${coreInstance}"] path`)
 const restart = cpu.locator(`g[data-instance="${restartedInstance}"] path`)
 // Core's first instance has two segments. The new instance is a separate sampled point.
 await expect(original).toHaveAttribute('d', /^(M[^ML]+L[^ML]+){2}$/)
 await expect(restart).toHaveAttribute('d', /^M[^ML]+h0\.01$/)

 const sample = cpu.getByRole('slider', { name: 'CPU 使用量采样点' })
 const readout = cpu.getByLabel('CPU 使用量样本')
 await expect(readout.getByTitle('0.00412345 核', { exact: true })).toContainText('0.0041 核')
 await expect(readout.getByTitle(restartedInstance, { exact: true })).toContainText(restartedInstance.slice(0, 8))
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
 await expect(page).toHaveURL(/tab=performance&window=6h$/)
 await expect(cpu.getByText('14:00', { exact: true })).toBeVisible()
 await page.reload()
 await expect(range).toHaveValue('6h')
 await expect(cpu.getByText('14:00', { exact: true })).toBeVisible()
 expect(requests).toEqual(['1h', '6h', '6h'])
 await page.setViewportSize({ width: 390, height: 820 })
 expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)

 await page.goBack()
 await page.goBack()
 await expect(dimensions.getByRole('link', { name: 'Topic', exact: true })).toHaveAttribute('aria-current', 'page')
 await expect(page.getByRole('combobox', { name: '时间范围', exact: true })).toHaveValue('7d')
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
 await page.goto('/observer/?perspective=metrics&tab=performance')
 await expect(page.getByRole('status')).toHaveText('正在读取系统性能…')
 release()
 await expect(page.getByRole('region', { name: titles[0], exact: true })).toContainText('当前范围暂无样本。')
 await expect(page.getByRole('region', { name: titles[1], exact: true }).getByRole('status')).toHaveText('该指标暂不可用。')
 await expect(page.getByRole('region', { name: titles[2], exact: true }).getByRole('slider')).toBeVisible()
 await page.getByRole('combobox', { name: '性能时间范围', exact: true }).selectOption('24h')
 await expect(page.getByRole('alert')).toHaveText('读取失败（HTTP 503）')
 await expect(page.getByRole('region', { name: titles[2], exact: true })).toHaveCount(0)
})
