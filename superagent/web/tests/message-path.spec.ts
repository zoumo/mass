import { writeFile } from 'node:fs/promises'
import { test, expect, type APIRequestContext } from '@playwright/test'

const audit = 'http://127.0.0.1:' + (process.env.SUPERAGENT_OBSERVER_AUDIT_PORT || '18795')
const standard = 'http://127.0.0.1:' + (process.env.SUPERAGENT_ROUTING_BROWSER_PORT || '18794')
type Message = { id: string, sourceMessageId: string, echo?: { match: { outboxId: string } } }
type Delivery = {
  id: string, kind: string, status: string, topicId?: string, sender: { kind: string }, recipient: { kind: string },
  createdAt: string, sentAt?: string, content: { body: string }, returnedDeliveryId?: string
}
type Issue = { id: string, title: string, topicId: string, materials: { messageId?: string }[] }
async function get<T>(request: APIRequestContext, url: string): Promise<T> {
  const response = await request.get(url)
  expect(response.ok(), url).toBeTruthy()
  return response.json()
}
async function findMessage(request: APIRequestContext, origin: string, nativeID: string) {
  const { items } = await get<{ items: Message[] }>(request, origin + '/v1/observer/messages?limit=200')
  return items.find(row => row.sourceMessageId === nativeID)!
}
async function sampleOrigin(request: APIRequestContext) {
  return (await get<{ endpoint: string }>(request, audit + '/message-path-fixture')).endpoint
}

test('Message branches use the sender, acceptance and explicit return ID committed by Core', async ({ request, page }, testInfo) => {
  const origin = await sampleOrigin(request)
  const msg = await findMessage(request, origin, 'observer-path-rerouted')
  const api = origin + '/v1/observer/'
  const routes = (await get<{ items: Delivery[] }>(request, api + 'routing-deliveries?messageId=' + msg.id)).items
  const returned = routes.find(row => row.returnedDeliveryId)!
  const original = routes.find(row => row.id === returned.returnedDeliveryId)!
  expect(original.status).toBe('superseded')
  const redispatched = routes.filter(row => row.sender.kind === 'router' && row.recipient.kind === 'topic')
  expect(new Set(redispatched.map(row => row.topicId)).size).toBe(2)
  await page.setViewportSize({ width: 1380, height: 960 })
  await page.goto(origin + '/observer/?perspective=message&id=' + msg.id)
  const graph = page.getByRole('region', { name: '消息路径' })
  const panel = page.getByRole('region', { name: '路径详情' })
  await expect(graph.locator('[data-delivery-id]')).toHaveCount(routes.length)
  await expect(graph.locator('[data-target-delivery="' + original.id + '"]').locator('[data-origin-id]'))
    .toHaveAttribute('data-origin-id', original.id)
  for (const d of [original, returned, ...redispatched]) {
    const edge = graph.locator('[data-delivery-id="' + d.id + '"]')
    await edge.focus()
    await page.keyboard.press('Enter')
    await expect(panel).toBeFocused()
    await expect(panel).toContainText(d.id)
    await expect(panel).toContainText(d.createdAt)
    await expect(panel).toContainText(d.content.body)
    await expect(panel).toContainText(d.sender.kind === 'core' ? 'Core' : d.sender.kind === 'router' ? 'Router' : 'Topic')
    if (d.sentAt) await expect(panel).toContainText(d.sentAt)
    await expect(edge).toHaveText(new RegExp(d.status === 'sent' ? 'Session 已受理' : '已被替代'))
    if (d.id === returned.id) await expect(panel).toContainText(original.id)
    await page.keyboard.press('Escape')
    await expect(edge).toBeFocused()
  }

  const scoped = api + 'issues?messageId=' + msg.id
  const first = await get<{ items: Issue[], nextCursor: string }>(request, scoped + '&limit=6')
  expect(first.items).toHaveLength(6)
  const next = await get<{ items: Issue[] }>(request, scoped + '&limit=6&cursor=' + encodeURIComponent(first.nextCursor))
  expect(next.items).toHaveLength(2)
  expect((await request.get(api + 'issues?messageId=elsewhere&cursor=' + encodeURIComponent(first.nextCursor))).status()).toBe(400)
  const hits = [...first.items, ...next.items]
  expect(hits.every(row => row.materials.some(material => material.messageId === msg.id))).toBe(true)
  await graph.getByRole('button', { name: '加载更多Issue 引用' }).click()
  await expect(graph.getByRole('button', { name: '引用本消息', exact: true })).toHaveCount(hits.length)
  for (const hit of hits) {
    const branch = graph.locator('li[data-target-delivery]').filter({ has: page.getByRole('button', { name: 'Issue ' + hit.title, exact: true }) })
    const id = await branch.getAttribute('data-target-delivery')
    expect(routes.find(d => d.id === id)!.topicId).toBe(hit.topicId)
  }
  await expect(graph.getByText('归档旧版本日志')).toHaveCount(0)
  await graph.getByRole('button', { name: 'Issue ' + next.items[0].title, exact: true }).click()
  await expect(panel).toContainText(msg.id)
  await panel.getByRole('link', { name: '查看此 Issue' }).click()
  await expect(page).toHaveURL(new RegExp('perspective=issue&id=' + next.items[0].id))
  await page.goBack()
  const target = redispatched[0]
  await graph.locator('[data-target-delivery="' + target.id + '"] .path-topic').click()
  await expect(panel).toContainText(target.topicId!)
  await panel.getByRole('link', { name: '查看此 Topic' }).click()
  await expect(page).toHaveURL(new RegExp('perspective=topic&id=' + target.topicId))
  await page.goBack()
  await graph.locator('[data-delivery-id="' + returned.id + '"]').click()
  await expect(panel).toContainText(returned.content.body)
  await page.screenshot({ path: testInfo.outputPath('message-branches-desktop.png'), animations: 'disabled', fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(graph.locator('.path-trunk')).toHaveCSS('flex-direction', 'column')
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
  await page.screenshot({ path: testInfo.outputPath('message-branches-narrow.png'), animations: 'disabled', fullPage: true })
  const evidence = testInfo.outputPath('route-evidence.json')
  await writeFile(evidence, JSON.stringify({ message: msg.id, routes, references: hits.map(({ id, topicId }) => ({ id, topicId })) }, null, 2))
  await testInfo.attach('route-evidence', { path: evidence, contentType: 'application/json' })
})

test('direct, pending, ignored, echo and channel acknowledgements stop at their recorded endpoints', async ({ page, request }) => {
  const extra = await sampleOrigin(request)
  const direct = await findMessage(request, extra, 'observer-path-direct')
  const graph = page.getByRole('region', { name: '消息路径' })
  await page.goto(extra + '/observer/?perspective=message&id=' + direct.id)
  await expect(graph.getByRole('button', { name: /直投/ })).toBeVisible()
  await expect(graph.getByRole('button', { name: 'Router', exact: true })).toHaveCount(0)
  await expect(graph.getByText('尚无 Issue 材料引用本消息')).toBeVisible()

  const missingRoute = await findMessage(request, extra, 'observer-path-material-only')
  await page.goto(extra + '/observer/?perspective=message&id=' + missingRoute.id)
  await expect(graph.getByRole('heading', { name: '材料引用 · 缺少对应 Topic 投递记录' })).toBeVisible()
  await expect(graph.locator('[data-target-delivery]')).toHaveCount(0)
  await graph.getByRole('button', { name: 'Issue 留存材料核对' }).click()
  await expect(page.getByRole('region', { name: '路径详情' })).toContainText(missingRoute.id)

  const pending = await findMessage(request, standard, 'outbox-observer-source')
  await page.goto(standard + '/observer/?perspective=message&id=' + pending.id)
  await expect(graph.getByText('待决策', { exact: true })).toBeVisible()
  await expect(graph.getByRole('button', { name: 'Router', exact: true })).toBeVisible()
  await expect(graph.locator('.path-topic')).toHaveCount(0)
  const replies = graph.locator('.path-outbox')
  await expect(replies.getByRole('heading')).toContainText('渠道回执 · DWS')
  await replies.getByText('渠道已发送').click()
  await expect(replies).toContainText('observer-actual-message')

  const ignored = await findMessage(request, standard, 'observer-routing')
  await page.goto(standard + '/observer/?perspective=message&id=' + ignored.id)
  await expect(graph.getByText('已忽略 · 原因未记录', { exact: true })).toBeVisible()
  const echo = await findMessage(request, standard, 'observer-actual-message')
  await page.goto(standard + '/observer/?perspective=message&id=' + echo.id)
  await expect(graph.getByText('回声 · 自身回声')).toBeVisible()
  await expect(graph.getByRole('link', { name: '查看匹配回执' })).toHaveAttribute('href', new RegExp(echo.echo!.match.outboxId))
  await expect(graph.locator('[data-delivery-id], .path-router, .path-topic')).toHaveCount(0)
})


test('a failed reference page preserves known branches and does not announce an empty result', async ({ page, request }) => {
  const origin = await sampleOrigin(request)
  const msg = await findMessage(request, origin, 'observer-path-rerouted')
  await page.goto(origin + '/observer/?perspective=message&id=' + msg.id)
  const graph = page.getByRole('region', { name: '消息路径' })
  await page.getByRole('button', { name: '暂停自动刷新' }).click()
  const more = graph.getByRole('button', { name: '加载更多Issue 引用' })
  const match = '**/v1/observer/issues?*cursor=*'
  await page.route(match, route => route.fulfill({ status: 503, body: '{}' }))
  await more.click()
  await expect(graph.getByRole('status')).toContainText('读取失败（HTTP 503）')
  await expect(graph.getByRole('button', { name: '引用本消息', exact: true })).toHaveCount(6)
  await expect(graph.getByText('尚无 Issue 材料引用本消息')).toHaveCount(0)
  await page.unroute(match)
  await more.click()
  await expect(graph.getByRole('button', { name: '引用本消息', exact: true })).toHaveCount(8)
  await expect(graph.getByRole('status')).toHaveCount(0)
})
