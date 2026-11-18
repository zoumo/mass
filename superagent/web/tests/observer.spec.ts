import { test, expect } from '@playwright/test'

test('committed lists, full original text, Thread navigation and safe rendering', async ({ page }) => {
 const bad: string[] = []
 page.on('pageerror', e => bad.push(e.message))
 const methods: string[] = []
 page.on('request', request => { if (request.url().includes('/v1/')) { methods.push(request.method()); expect(request.headers()['authorization']).toBeUndefined() } })
 await page.goto('/observer/?view=messages&mode=records')
 await expect(page.getByText('核心服务已就绪', { exact: true })).toBeVisible()
 await expect(page.getByText('显示 50 条消息', { exact: false })).toBeVisible()
 await page.getByRole('button', { name: /^Browser original body/ }).click()
 await expect(page.getByTestId('message-body')).toContainText('Long original text '.repeat(1500))
 expect(await page.evaluate(() => (window as unknown as { attack?: boolean }).attack)).toBeUndefined()
 await expect(page.getByRole('dialog')).toContainText('已接收')
 await page.getByText('原始字段和引用', { exact: true }).click()
 await expect(page.getByRole('dialog')).toContainText('sourceMessageId')
 const selected = page.url()
 await page.reload()
 await expect(page.getByTestId('message-body')).toBeVisible()
 expect(page.url()).toBe(selected)
 await page.getByRole('button', { name: /^查看线程 dws:/ }).click()
 await expect(page.getByRole('dialog')).toContainText('线程详情')
 await page.getByRole('button', { name: '查看线程消息' }).click()
 await expect(page.getByRole('textbox', { name: '按线程键筛选' })).toHaveValue(/^dws:/)
 await expect(page.getByText('显示 50 条消息', { exact: false })).toBeVisible()
 await page.getByRole('button', { name: '加载更多' }).click()
 await expect(page.getByText('显示 56 条消息', { exact: false })).toBeVisible()
 await expect(page.getByRole('button', { name: '加载更多' })).toHaveCount(0)
 expect(bad).toEqual([])
 expect(methods.every(method => method === 'GET')).toBeTruthy()
})

test('read-only API, source isolation, cursor validation and HEAD', async ({ request }) => {
 for (const collection of ['messages', 'threads', 'events', 'health', 'channel-health']) {
  const read = await request.get(`/v1/observer/${collection}`)
  expect(read.status()).toBe(200)
  const head = await request.head(`/v1/observer/${collection}`)
  expect(head.status()).toBe(200)
  expect((await head.body()).length).toBe(0)
  for (const method of ['POST', 'PUT', 'PATCH', 'DELETE']) {
   const response = await request.fetch(`/v1/observer/${collection}`, { method })
   expect(response.status()).toBe(405)
  }
 }
 expect((await request.post('/v1/channel/messages', { data: {} })).status()).toBe(401)
 expect((await request.get('/v1/observer/messages?cursor=invalid')).status()).toBe(400)
 expect((await request.get('/v1/observer/messages', { headers: { Origin: 'https://foreign.example' } })).status()).toBe(403)
 const event = (await (await request.get('/v1/observer/events')).json()).items[0]
 expect(event.status).toBe('pending')
 expect(event.intent).toBe('act')
})

test('channel health shows reported disconnect, failed admission and buffered count', async ({ page }) => {
 const channelMethods: string[] = []
 page.on('request', request => {
  if (!request.url().includes('/channel-health')) return
  channelMethods.push(request.method())
  expect(request.headers()['authorization']).toBeUndefined()
 })
 await page.goto('/observer/')
 await page.getByText('诊断', { exact: true }).click()
 await page.getByRole('button', { name: '渠道健康状态', exact: true }).click()
 const row = page.getByRole('row', { name: /browser-collector/ })
 await expect(row).toContainText('已断开')
 await expect(row).toContainText('100 / 100')
 await row.getByRole('button', { name: 'browser-collector' }).click()
 const drawer = page.getByRole('dialog')
 await expect(drawer).toContainText('渠道健康状态详情')
 await expect(drawer).toContainText('失败 · 100 / 100 已缓冲')
 await expect(drawer).toContainText('不可用')
 await expect(drawer).toContainText('到期时间')
 expect(channelMethods.length).toBeGreaterThanOrEqual(2)
 expect(channelMethods.every(method => method === 'GET')).toBeTruthy()
})

test('offline snapshot stays visible and reconnect gets fresh filtered pages and selected detail', async ({ page, context, request }, testInfo) => {
 await page.goto('/observer/?view=messages&mode=records')
 const first = (await (await request.get('/v1/observer/messages?limit=1')).json()).items[0]
 const filter = page.getByRole('textbox', { name: '按线程键筛选' })
 await filter.fill(first.threadKey)
 await page.getByRole('button', { name: '筛选', exact: true }).click()
 await page.getByRole('button', { name: '加载更多' }).click()
 await expect(page.getByText('显示 56 条消息')).toBeVisible()
 await page.getByRole('button', { name: 'Browser row 54', exact: true }).click()
 await expect(page.getByTestId('message-body')).toHaveText('Browser row 54')
 await context.setOffline(true)
 await expect(page.getByRole('alert')).toContainText('快照已过时')
 await expect(page.getByTestId('message-body')).toHaveText('Browser row 54')
 const source = { source: first.source, accountId: first.accountId, actor: first.actor, addressed: first.addressed }
 const submit = async (id: string, threadRef: typeof first.threadRef) => request.post('/v1/channel/messages', {
  headers: { Authorization: `Bearer ${'a'.repeat(48)}` },
  data: { operationId: id, message: { ...source, threadRef, sourceMessageId: id, text: id } },
 })
 expect((await submit('Committed while offline', first.threadRef)).ok()).toBeTruthy()
 expect((await submit('Unrelated Thread', { ...first.threadRef, id: 'unrelated-thread' })).ok()).toBeTruthy()
 await context.setOffline(false)
 await expect(page.getByRole('alert')).toHaveCount(0)
 await expect(page.getByTestId('message-body')).toHaveText('Browser row 54')
 await page.getByRole('button', { name: '关闭', exact: true }).click()
 await expect(filter).toHaveValue(first.threadKey)
 await expect(page.getByText('显示 57 条消息')).toBeVisible()
 await expect(page.getByRole('button', { name: 'Committed while offline', exact: true })).toBeVisible()
 await expect(page.getByRole('button', { name: 'Unrelated Thread', exact: true })).toHaveCount(0)
 await expect(page.getByRole('button', { name: '加载更多' })).toHaveCount(0)
 await page.screenshot({ path: testInfo.outputPath('reconnected-filter.png'), fullPage: true, animations: 'disabled' })
})
