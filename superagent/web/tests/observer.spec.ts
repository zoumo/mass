import { test, expect } from '@playwright/test'

test('shows structured Message body and attachment types while retaining Thread navigation', async ({ page, request }) => {
 const result = await (await request.get('/v1/core/trigger-messages?limit=1')).json()
 const { id } = result.items[0]
 let content = [
  { type: 'text', text: 'Inspect the attached log.' },
  { type: 'resource_link', uri: 'file:///checks.log', name: 'checks.log' },
  { type: 'text', text: 'Confirm the exit code.' },
 ]
 await page.route(`**/v1/core/trigger-messages/${id}*`, async route => {
  const response = await route.fetch()
  const record = await response.json()
  await route.fulfill({ response, json: { ...record, content } })
 })
 const url = `/superagent/?perspective=message&id=${id}`
 await page.goto(url)
 await page.getByText('查看全文', { exact: true }).click()
 await expect(page.getByTestId('message-body')).toHaveText('Inspect the attached log.\nConfirm the exit code.')
 await expect(page.getByRole('link', { name: '查看线程' })).toBeVisible()

 content = [content[1]]
 await page.reload()
 await page.getByText('查看全文', { exact: true }).click()
 await expect(page.getByTestId('message-body')).toHaveText('[resource_link]')
})

test('committed lists, full original text, Thread navigation and safe rendering', async ({ page }) => {
 const bad: string[] = []
 page.on('pageerror', e => bad.push(e.message))
 const methods: string[] = []
 page.on('request', request => { if (request.url().includes('/v1/')) { methods.push(request.method()); expect(request.headers()['authorization']).toBeUndefined() } })
 await page.goto('/superagent/?view=messages&mode=records')
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
 for (const collection of ['threads', 'internal-events']) {
  const read = await request.get(`/v1/core/${collection}`)
  expect(read.status()).toBe(200)
  const head = await request.head(`/v1/core/${collection}`)
  expect(head.status()).toBe(200)
  expect((await head.body()).length).toBe(0)
  for (const method of ['POST', 'PUT', 'PATCH', 'DELETE']) {
   const response = await request.fetch(`/v1/core/${collection}`, { method })
   expect(response.status()).toBe(405)
  }
 }
 expect((await request.post('/v1/core/trigger-messages', { data: {} })).status()).toBe(401)
 expect((await request.get('/v1/core/trigger-messages?cursor=invalid')).status()).toBe(400)
 expect((await request.get('/v1/core/trigger-messages', { headers: { Origin: 'https://foreign.example' } })).status()).toBe(403)
 const messages = (await (await request.get('/v1/core/trigger-messages')).json()).items
 const event = (await (await request.get('/v1/core/internal-events?messageId=' + messages[0].id)).json()).items[0]
 expect(event.status).toBe('pending')
 expect(event.kind).toBe('route_request')
})

test('copies the persisted trace ID from a selected record', async ({ page, context, request, baseURL }) => {
 await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: baseURL })
 const records = await (await request.get('/v1/core/trigger-messages?limit=1')).json()
 const id = records.items[0].id
 const detail = await (await request.get(`/v1/core/trigger-messages/${id}?trace=true`)).json()
 expect(detail.traceAssociations).toEqual([{ phase: 'admission', traceId: expect.stringMatching(/^[0-9a-f]{32}$/) }])
 await page.goto(`/superagent/?view=messages&mode=records&id=${id}`)
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByRole('heading', { name: '关联 Trace' })).toBeVisible()
 await dialog.getByRole('button', { name: '复制 Trace ID' }).click()
 await expect(dialog.getByRole('button', { name: '已复制' })).toBeVisible()
 expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(detail.traceAssociations[0].traceId)
})

test('retired collections are absent from Superagent Web', async ({ page, request }) => {
 await page.goto('/superagent/')
 await page.getByText('诊断', { exact: true }).click()
 await expect(page.getByRole('button', { name: '内部事件', exact: true })).toBeVisible()
 await expect(page.getByRole('button', { name: '渠道健康状态', exact: true })).toHaveCount(0)
 for (const collection of ['pull-requests', 'subscriptions', 'relations', 'routing-events', 'channel-health', 'events', 'decisions', 'deliveries', 'batches', 'attempts', 'recoveries']) {
  expect((await request.get(`/v1/core/${collection}`)).status()).toBe(404)
 }
 expect((await request.post('/v1/channel/heartbeat', { data: {} })).status()).toBe(404)
})

test('offline snapshot stays visible and reconnect gets fresh filtered pages and selected detail', async ({ page, context, request }, testInfo) => {
 await page.goto('/superagent/?view=messages&mode=records')
 const first = (await (await request.get('/v1/core/trigger-messages?limit=1')).json()).items[0]
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
 const submit = async (id: string, threadKey: string) => request.post('/v1/core/trigger-messages', {
  headers: { Authorization: `Bearer ${'a'.repeat(48)}` },
  data: { operationId: id, message: { threadKey, sourceMessageId: id, content: [{ type: 'text', text: id }] } },
 })
 expect((await submit('Committed while offline', first.threadKey)).ok()).toBeTruthy()
 expect((await submit('Unrelated Thread', 'dws:unrelated-thread')).ok()).toBeTruthy()
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
