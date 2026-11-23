import { expect, test } from '@playwright/test'

const corePort = process.env.SUPERAGENT_ROUTING_GATEWAY_PORT || '18894'
const audit = `http://127.0.0.1:${process.env.SUPERAGENT_OBSERVER_AUDIT_PORT || '18795'}`
test.use({ baseURL: `http://127.0.0.1:${corePort}`, viewport: { width: 1340, height: 980 } })

test('Message and Thread lead to scoped work, reports, explicit acceptance and Session delivery', async ({ page }, testInfo) => {
 await page.goto('/superagent/?view=messages&mode=records')
 const drawer = page.getByRole('dialog')
 const overview = page.getByRole('region', { name: '运行概况' })
 await expect(overview.getByText('路由器准备状态')).toBeVisible()
 await expect(overview.getByText(/1 进行中 · 1 已完成/)).toBeVisible()
 await expect(overview.getByText(/1 受阻 · 1 已取消 · 1 已完成/)).toBeVisible()
 await expect(overview.getByText(/2 失败/)).toBeVisible()
 await page.getByRole('button', { name: 'Original input observer-routing', exact: true }).click()
 const routing = drawer.getByRole('region', { name: '路由历史' })
 await expect(routing).toContainText('Observer return reason')
 await expect(routing.getByRole('button', { name: /^查看 Topic mass-t[1-9]\d*$/ }).first()).toBeVisible()
 await drawer.getByRole('button', { name: /^查看线程 dws:/ }).click()
 await drawer.getByRole('button', { name: '查看线程消息' }).click()
 await expect(page).toHaveURL(/view=messages&threadKey=/)
 await page.getByRole('button', { name: 'Original input observer-routing', exact: true }).click()
 await drawer.getByRole('button', { name: /^查看 Topic mass-t[1-9]\d*$/ }).first().click()
 await expect(drawer.getByRole('heading', { name: 'Observer routing Topic' })).toBeVisible()
 await drawer.getByRole('button', { name: '查看 Topic 下的 Issue' }).click()
 await page.getByRole('button', { name: 'Review blocked build' }).click()
 await drawer.getByRole('button', { name: '当前会话', exact: true }).click()
 await expect(page).toHaveURL(/view=session-bindings&id=/)
 await expect(drawer.getByText('准备状态', { exact: true }).locator('xpath=following-sibling::dd[1]')).toContainText('已就绪')
 await expect(drawer.getByText('Session 已受理', { exact: true })).toBeVisible()
 await page.goBack()
 await expect(drawer.getByRole('heading', { name: 'Review blocked build' })).toBeVisible()
 await drawer.getByRole('button', { name: /^查看依赖的 Issue mass-t[1-9]\d*-i[1-9]\d*$/ }).click()
 await drawer.getByRole('button', { name: '报告历史' }).click()
 await page.getByRole('button', { name: 'Sample build verified' }).click()
 await expect(drawer.getByText(/^已就绪 · /)).toBeVisible()
 await drawer.getByRole('button', { name: 'Topic 通知' }).click()
 await expect(drawer.getByText('Session 已受理', { exact: true })).toBeVisible()
 await drawer.getByRole('button', { name: '关闭', exact: true }).click()
 await page.getByRole('navigation').getByRole('button', { name: 'Topic', exact: true }).click()
 await page.getByRole('button', { name: 'Archived sample Topic' }).click()
 await expect(drawer.getByText('已完成', { exact: true })).toBeVisible()
 await expect(drawer.getByText('Reviewed the archived result', { exact: true })).toBeVisible()
 await expect(drawer.getByText('artifacts/archive.log', { exact: true })).toBeVisible()
 await page.screenshot({ path: testInfo.outputPath('topic-completed.png'), fullPage: true, animations: 'disabled' })
})

test('Session reconnect rereads durable history while browsing produces no dependency writes', async ({ page, request }, testInfo) => {
 const state = async () => (await request.get(`${audit}/state`)).json() as Promise<{
  rpc: Record<string, number>; polls: number; sentOffers: number; pendingOffers: number; channelSends: number; sentId: string; sessionBindingId: string
 }>
 const before = await state()
 const sentPath = `/v1/core/outbox-messages/${before.sentId}`
 const sentBefore = await (await request.get(sentPath)).json()
 expect(sentBefore).toMatchObject({
  status: 'sent', receipt: { taskId: 'observer-send-task' },
 })
 const errors: string[] = []
 const headers: Promise<Record<string, string>>[] = []
 await page.context().addCookies([{ name: 'observer_fixture', value: 'browser-cookie', url: `http://127.0.0.1:${corePort}` }])
 const documentRequest = page.waitForRequest(r => new URL(r.url()).pathname === '/superagent/')
 page.on('pageerror', error => errors.push(error.message))
 page.on('request', r => {
  const path = new URL(r.url()).pathname
  if (!path.startsWith('/v1/')) return
  expect(path).toMatch(/^\/v1\/core\//)
  expect(r.method()).toBe('GET')
  headers.push(r.allHeaders())
 })
 await page.goto(`/superagent/?view=session-bindings&id=${before.sessionBindingId}`)
 expect((await (await documentRequest).allHeaders())['cookie']).toContain('observer_fixture=browser-cookie')
 const history = page.getByRole('region', { name: 'Session 历史' })
 const live = page.getByRole('region', { name: 'Session 实时预览' })
 await expect(history.getByText('Stored before browsing <b>plain text</b> [redacted] [redacted]', { exact: true })).toBeVisible()
 await expect(history.locator('pre b')).toHaveCount(0)
 await expect(live.getByText('实时连接', { exact: true })).toBeVisible()
 expect((await request.post(`${audit}/emit-live`)).status()).toBe(204)
 await expect(live.getByText('Live before disconnect [redacted] [redacted]', { exact: true })).toBeVisible()
 await expect(history.getByText(/Live before disconnect/)).toHaveCount(0)
 const historyRoute = `**/v1/core/session-bindings/${before.sessionBindingId}/history?*`
 await page.route(historyRoute, route => route.abort())
 expect((await request.post(`${audit}/emit-stored`)).status()).toBe(204)
 await expect(history.getByText(/无法读取历史/)).toBeVisible()
 await expect(history.getByText('Stored while live', { exact: true })).toHaveCount(0)
 await page.unroute(historyRoute)
 await history.getByRole('button', { name: '刷新 Session 历史' }).click()
 await expect(history.getByText('Stored while live', { exact: true })).toBeVisible()
 expect((await request.post(`${audit}/disconnect`)).status()).toBe(204)
 await expect(live.getByText(/观测中断/)).toBeVisible()
 await expect(live.getByText('实时连接', { exact: true })).toBeVisible()
 await expect(history.getByText('Stored during disconnect', { exact: true })).toBeVisible()
 await expect(page.getByText('Transient during disconnect', { exact: true })).toHaveCount(0)
 await page.screenshot({ path: testInfo.outputPath('session-reconnected.png'), fullPage: true, animations: 'disabled' })
 await page.reload()
 await expect(history.getByText('Stored during disconnect', { exact: true })).toBeVisible()
 await live.getByText('实时连接', { exact: true }).waitFor()
 await page.getByRole('button', { name: '关闭', exact: true }).click()
 await page.getByRole('button', { name: '刷新', exact: true }).click()
 await page.getByRole('navigation').getByRole('button', { name: '回执', exact: true }).click()
 await page.getByRole('button', { name: 'Observer reply sent', exact: true }).click()
 await expect(page.getByRole('dialog').getByText('已发送', { exact: true })).toBeVisible()
 await page.getByRole('button', { name: '查看目标消息', exact: true }).click()
 await expect(page.getByTestId('message-body')).toHaveText('Original input outbox-observer-source')
 const previewPath = `/v1/core/session-bindings/${before.sessionBindingId}`
 for (const path of [`${previewPath}/history`, `${previewPath}/live`, sentPath, '/superagent/']) {
  const head = await request.head(path)
  expect(head.status()).toBe(200)
  expect(await head.body()).toHaveLength(0)
  const result = await request.post(path, { data: {} })
  expect(result.status()).toBe(405)
  const allowed = result.headers()['allow'].split(',').map(method => method.trim())
  expect(allowed.sort()).toEqual(path === sentPath ? ['GET', 'HEAD', 'PATCH'] : ['GET', 'HEAD'])
 }
 const toolCall = await request.post('/v1/mcp', {
  headers: { Accept: 'application/json, text/event-stream', 'MCP-Protocol-Version': '2025-11-25' },
  data: { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'report_submit', arguments: {} } },
 })
 expect(toolCall.status()).toBe(401)
 expect((await request.patch(sentPath, { data: {} })).status()).toBe(401)
 const after = await state()
 const methods = Object.fromEntries(Object.entries(after.rpc)
  .map(([method, count]) => [method, count - (before.rpc[method] ?? 0)] as const)
  .filter(([, delta]) => delta > 0))
 const reads = ['initialize', 'initialized', 'runtime.list', 'session.get', 'session.history', 'session.subscribe', 'session.unsubscribe']
 expect(Object.keys(methods).filter(method => !reads.includes(method))).toEqual([])
 expect(methods['runtime.list']).toBeGreaterThan(0)
 expect(methods['session.history']).toBeGreaterThanOrEqual(3)
 expect(methods['session.subscribe']).toBeGreaterThanOrEqual(3)
 expect(after.polls).toBeGreaterThan(before.polls)
 expect(after.pendingOffers).toBeGreaterThan(before.pendingOffers)
 expect(after.sentOffers).toBe(0)
 expect(before.channelSends).toBe(1)
 expect(after.channelSends).toBe(before.channelSends)
 expect(await (await request.get(sentPath)).json()).toEqual(sentBefore)
 expect(headers.length).toBeGreaterThan(0)
 for (const header of await Promise.all(headers)) {
  expect(header['authorization']).toBeUndefined()
  expect(header['cookie']).toBeUndefined()
 }
 expect(errors).toEqual([])
 await testInfo.attach('dependency-calls', {
  body: JSON.stringify({
   methods, newChannelPolls: after.polls - before.polls, pendingOffers: after.pendingOffers - before.pendingOffers,
   sentOffers: after.sentOffers, channelSends: { before: before.channelSends, after: after.channelSends }, sentReceipt: sentBefore.receipt,
  }, null, 2),
  contentType: 'application/json',
 })
})
