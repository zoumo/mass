import { test, expect, type APIRequestContext } from '@playwright/test'
import { fixtureOrigins } from './endpoints.mjs'

const routing = 'http://127.0.0.1:' + (process.env.SUPERAGENT_ROUTING_GATEWAY_PORT || '18894')

type Message = { id: string; sourceMessageId: string; eventId: string }
type InternalEvent = { event_id: string; createdAt: string; kind: string; topicId?: string; returnedEventId?: string; body?: string }
type RouterRow = { id: string; state: string; activityAt: string; activityId: string; targets: { id: string; topicId: string; reason: string }[] }
async function list<T>(request: APIRequestContext, url: string): Promise<T[]> {
  const result = await request.get(url)
  expect(result.ok()).toBeTruthy()
  return (await result.json()).items
}

test('Router lists distinct Messages with a stable cursor while a new Message arrives', async ({ page, request }) => {
  const before = await list<RouterRow>(request, '/v1/core/trigger-messages?view=router&limit=100')
  expect(before.length).toBeGreaterThan(50)
  const tiedAt = before.find((row, i) => i > 0 && row.activityAt === before[i - 1].activityAt)!.activityAt
  const tiedIDs = before.filter(row => row.activityAt === tiedAt).map(row => row.id)
  expect(tiedIDs).toEqual([...tiedIDs].sort().reverse())
  await page.goto('/superagent/router')
  await page.getByRole('button', { name: '暂停自动刷新' }).click()
  const rows = page.locator('[data-message-id]')
  await expect(rows).toHaveCount(20)
  await expect(rows.first()).toContainText('待决策')
  const flow = page.getByRole('region', { name: 'Router 处理流程' })
  await expect(flow.locator('.flow-state').first()).toHaveText(/待决策/)
  await expect(flow.locator('.flow-topic')).toHaveCount(0)
  const insert = await request.post('/v1/core/trigger-messages', {
    headers: { Authorization: 'Bearer ' + 'a'.repeat(48) },
    data: { operationId: 'router-browser-insert', message: {
      sourceMessageId: 'router-browser-insert', threadId: 'dws:observer-router-new-message',
      content: [{ type: 'text', text: '翻页期间收到的新消息' }],
    } },
  })
  expect(insert.ok()).toBeTruthy()
  const { messageId } = await insert.json()
  await page.getByRole('button', { name: '加载更多消息' }).click()
  await expect(rows).toHaveCount(40)
  expect(await rows.evaluateAll(items => items.map(el => el.getAttribute('data-message-id')))).toEqual(before.slice(0, 40).map(r => r.id))
  await page.getByRole('combobox', { name: '每页' }).selectOption('50')
  await expect(rows).toHaveCount(50)
  await expect(rows.first()).toHaveAttribute('data-message-id', messageId)
  await page.getByRole('combobox', { name: '每页' }).selectOption('100')
  await expect(rows).toHaveCount(before.length + 1)
  const tiedRows = rows.filter({ has: page.locator(`time[datetime="${tiedAt}"]`) })
  expect(await tiedRows.evaluateAll(items => items.map(el => el.getAttribute('data-message-id')))).toEqual(tiedIDs)
  await page.reload()
  await expect(page.getByRole('combobox', { name: '每页' })).toHaveValue('100')
})

test('Router connects each target and return reason to its source event', async ({ page, request }, testInfo) => {
  const endpoint = fixtureOrigins['message-path-fixture']
  const api = endpoint + '/v1/core/'
  const messages = await list<Message>(request, api + 'trigger-messages?preview=true')
  const bySource = (id: string) => messages.find(m => m.sourceMessageId === id)!
  const direct = bySource('observer-path-direct')
  const rerouted = bySource('observer-path-rerouted')
  const partial = bySource('observer-path-partial-return')
  const result = await list<RouterRow>(request, api + 'trigger-messages?view=router')
  const summary = result.find(row => row.id === partial.id)!
  expect(result.some(row => row.id === direct.id)).toBe(false)
  expect(result.filter(row => row.id === rerouted.id)).toHaveLength(1)
  expect(result.filter(row => row.id === partial.id)).toHaveLength(1)
  const internalEvents = await list<InternalEvent>(request, api + 'internal-events?messageId=' + partial.id)
  const returned = internalEvents.find(d => d.returnedEventId)!
  const original = internalEvents.find(d => d.event_id === returned.returnedEventId)!
  const requests: string[] = []
  page.on('request', req => {
    if (req.url().includes('/v1/')) {
      expect(req.method()).toBe('GET')
      expect(req.headers()['authorization']).toBeUndefined()
      requests.push(new URL(req.url()).pathname)
    }
  })
  await page.setViewportSize({ width: 1360, height: 900 })
  await page.goto(endpoint + '/superagent/router')
  const row = page.locator(`[data-message-id="${partial.id}"]`)
  await expect(row).toContainText('已分发')
  await expect(row.locator('.router-brief')).toHaveCount(2)
  for (const target of summary.targets) {
    const branch = row.locator('.router-brief').filter({ has: page.locator(`a[href*="${target.topicId}"]`) })
    const fact = internalEvents.find(d => d.event_id === target.id)!
    await expect(branch).toContainText(fact.body!)
    expect(fact.topicId).toBe(target.topicId)
  }
  await expect(row.locator('time')).toHaveAttribute('datetime', internalEvents.find(d => d.event_id === summary.activityId)!.createdAt)
  expect(requests.filter(path => path.endsWith('/routing-events'))).toHaveLength(0)
  await expect(page.locator(`[data-message-id="${bySource('observer-path-material-only').id}"]`)).toContainText('目标事件记录缺失')
  await page.screenshot({ path: testInfo.outputPath('router-overview.png'), animations: 'disabled' })
  const toggle = row.getByRole('button').first()
  await toggle.focus()
  await page.keyboard.press('Enter')
  const history = row.getByRole('region', { name: '路由事件记录' })
  await expect(history.locator('li')).toHaveCount(internalEvents.length)
  for (const d of internalEvents) {
    // Locate by the persisted ID, which also occurs inside the return reference.
    const entry = history.locator('li').filter({ has: page.getByText(d.event_id, { exact: true }) })
    if (d.body) await expect(entry.first()).toContainText(d.body)
    await expect(entry.first().locator('time').first()).toHaveAttribute('datetime', d.createdAt)
  }
  const ref = history.getByRole('button', { name: '原事件 ' + original.event_id })
  await ref.click()
  await expect(ref.locator('..').locator('dd').last()).toHaveText(original.body!)
  await ref.click()
  await row.evaluate(el => {
    const scroll = el.closest('.router-scroll')!
    scroll.scrollTop += el.getBoundingClientRect().top - scroll.getBoundingClientRect().top
  })
  await page.screenshot({ path: testInfo.outputPath('router-desktop.png'), animations: 'disabled' })
  await row.getByRole('link', { name: '查看消息路径' }).click()
  await expect(page).toHaveURL(new RegExp('/superagent/message\\?id=' + partial.id))
  await page.goBack()
  await expect(history).toBeVisible()
  await row.locator('.router-brief a').first().click()
  await expect(page).toHaveURL(new RegExp('/superagent/topic\\?id=' + summary.targets[0].topicId))
  await page.goBack()
  await expect(history).toBeVisible()
  await page.reload()
  await expect(history).toBeVisible()
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(row).toContainText('两个 Topic 各自核对')
  await page.screenshot({ path: testInfo.outputPath('router-narrow.png'), animations: 'disabled' })
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
  await toggle.focus()
  await page.keyboard.press('Enter')
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await expect(toggle).toBeFocused()
})

test('ignored Message displays the recorded return time and identifies the missing ignore reason', async ({ page, request }) => {
  const messages = await list<Message>(request, routing + '/v1/core/trigger-messages?preview=true')
  const ignored = messages.find(m => m.sourceMessageId === 'observer-routing')!
  const internalEvents = await list<InternalEvent>(request, routing + '/v1/core/internal-events?messageId=' + ignored.id)
  const returned = internalEvents.find(d => d.returnedEventId)!
  await page.goto(routing + '/superagent/router?id=' + ignored.id)
  const row = page.locator(`[data-message-id="${ignored.id}"]`)
  await expect(row.locator('.router-grid')).toContainText('已忽略')
  await expect(row.locator('.router-grid')).toContainText('原因未记录')
  await expect(row.locator('.router-brief')).toContainText(returned.body!)
  await expect(row.locator('.router-time')).toContainText('退回登记')
  await expect(row.locator('.router-time time')).toHaveAttribute('datetime', returned.createdAt)
  await expect(row).not.toContainText('Observer ignore reason')
  const flow = page.getByRole('region', { name: 'Router 处理流程' })
  await expect(flow.locator('.flow-state')).toContainText('已忽略 · 原因未记录')
  const returnLane = flow.locator(`[data-flow-id="${returned.event_id}"]`)
  await expect(returnLane.locator('.flow-node').last()).toHaveText('Router')
  await expect(returnLane.locator('.flow-edge').last()).toContainText(returned.body!)
})

for (const [size, width, height] of [['desktop', 1360, 900], ['narrow', 390, 844]] as const) {
  test(`Router navigation opens a populated flow diagram before the list (${size})`, async ({ page, request }, testInfo) => {
    const endpoint = fixtureOrigins['message-path-fixture']
    const api = endpoint + '/v1/core/'
    const messages = await list<Message>(request, api + 'trigger-messages?preview=true')
    const partial = messages.find(m => m.sourceMessageId === 'observer-path-partial-return')!
    const result = await list<RouterRow>(request, api + 'trigger-messages?view=router')
    const record = result.find(row => row.id === partial.id)!
    await page.setViewportSize({ width, height })
    await page.goto(endpoint + '/superagent/')
    await page.getByRole('navigation', { name: '观察视角' }).getByRole('link', { name: 'Router' }).click()
    const flow = page.getByRole('region', { name: 'Router 处理流程' })
    await expect(flow.getByRole('heading')).toBeInViewport()
    await expect(flow.locator('.flow-message').first()).toBeInViewport()
    await expect(flow).toContainText('已载入 ' + result.length + ' 条消息')
    const first = flow.locator('[data-flow-id]').first()
    await expect(first.locator('.flow-message')).toHaveAttribute('href', new RegExp(result[0].id))
    await expect(first.locator('.flow-edge').first()).toBeInViewport()
    await page.screenshot({ path: testInfo.outputPath('router-entry-flow-' + size + '.png'), animations: 'disabled' })
    const expand = flow.getByRole('button', { name: /再看/ })
    if (await expand.count()) await expand.click()
    for (const target of record.targets) {
      const lane = flow.locator(`[data-flow-id="${target.id}"]`)
      await expect(lane.locator('.flow-topic')).toHaveAttribute('href', new RegExp(target.topicId))
      await expect(lane.locator('.flow-edge').last()).toContainText(target.reason)
    }
    const row = page.locator(`[data-message-id="${partial.id}"]`)
    await row.getByRole('button').first().click()
    await expect(flow).toContainText('当前消息')
    await expect(flow.locator('[data-flow-id]')).toHaveCount(record.targets.length)
    const lane = flow.locator(`[data-flow-id="${record.targets[1].id}"]`)
    await lane.getByText('展开记录', { exact: true }).click()
    await expect(lane.getByRole('link', { name: /事件记录/ })).toHaveAttribute('href', new RegExp(record.targets[1].id))
    await expect(flow.locator('.flow-message').first()).toHaveAttribute('href', new RegExp(partial.id))
    await lane.locator('.flow-topic').click()
    await expect(page).toHaveURL(new RegExp('/superagent/topic\\?id=' + record.targets[1].topicId))
    await page.goBack()
    await expect(flow).toContainText('当前消息')
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width)
  })
}
