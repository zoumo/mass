import { test, expect } from '@playwright/test'

test.use({ baseURL: `http://127.0.0.1:${process.env.SUPERAGENT_ROUTING_BROWSER_PORT || '18794'}` })

test('routing, preparation, subscriptions and returns are visible through read-only navigation', async ({ page, request }, testInfo) => {
 const methods: string[] = []
 const errors: string[] = []
 page.on('pageerror', e => errors.push(e.message))
 page.on('request', r => { if (r.url().includes('/v1/')) { methods.push(r.method()); expect(r.headers()['authorization']).toBeUndefined() } })
 await page.goto('/observer/?view=topics&mode=records')
 await expect(page.getByText('核心服务已就绪', { exact: true })).toBeVisible()
 await page.getByRole('button', { name: 'Observer routing Topic', exact: true }).click()
 await expect(page.getByRole('dialog').getByText('进行中', { exact: true })).toBeVisible()
 await page.getByRole('button', { name: 'Session 准备记录', exact: true }).click()
 await expect(page.getByRole('dialog').getByText('已就绪', { exact: true })).toBeVisible()
 await expect(page.getByRole('dialog').getByText(/topic-g1/).first()).toBeVisible()
 await expect(page.getByRole('dialog')).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('routing-preparation.png'), fullPage: true, animations: 'disabled' })
 await page.getByRole('button', { name: '关闭', exact: true }).click()
 await page.getByRole('button', { name: '路由关系', exact: true }).click()
 await page.getByRole('button', { name: '已退回', exact: true }).click()
 await expect(page.getByTestId('routing-record')).toContainText('Observer return reason')
 await expect(page.getByTestId('routing-record')).toContainText('"eventId": "evt_')
 await expect(page.getByRole('dialog')).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('routing-return.png'), fullPage: true, animations: 'disabled' })
 await page.getByRole('button', { name: '关闭', exact: true }).click()
 await page.getByRole('button', { name: '订阅', exact: true }).click()
 await expect(page.getByText('显示 5 条订阅', { exact: false })).toBeVisible()
 await page.getByRole('table').getByRole('button', { name: /^sub_/ }).first().click()
 await expect(page.getByRole('dialog').getByRole('button', { name: /^查看线程 dws:/ })).toBeVisible()
 await page.getByRole('button', { name: '关闭', exact: true }).click()
 await page.getByRole('button', { name: '路由事件', exact: true }).click()
 await expect(page.getByRole('table').getByText('Session 已受理').first()).toBeVisible()
 await page.getByRole('table').getByRole('button', { name: '路由请求', exact: true }).first().click()
 await expect(page.getByTestId('routing-record')).toContainText('"kind": "route_request"')
 for (const collection of ['topics', 'routing-events', 'subscriptions', 'relations', 'bindings']) {
  expect((await request.get(`/v1/observer/${collection}`)).status()).toBe(200)
  expect((await request.head(`/v1/observer/${collection}`)).status()).toBe(200)
  expect((await request.post(`/v1/observer/${collection}`, { data: {} })).status()).toBe(405)
 }
 expect(errors).toEqual([])
 expect(methods.every(method => method === 'GET')).toBeTruthy()
})

test('internal event shows Session acceptance and stable identity', async ({ page, request }, testInfo) => {
 const methods: string[] = []
 page.on('request', r => { if (r.url().includes('/v1/')) methods.push(r.method()) })
 await page.goto('/observer/?view=internal-events&id=browser-accepted')
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByText('Session 已受理', { exact: true })).toBeVisible()
 await expect(page.getByTestId('routing-record')).toContainText('"event_id": "browser-accepted"')
 await expect(page.getByTestId('routing-record')).toContainText('"kind": "context_supplement"')
 await expect(dialog.getByRole('button', { name: /重试|替换|恢复|确认投递/ })).toHaveCount(0)
 await page.screenshot({ path: testInfo.outputPath('internal-event.png'), fullPage: true, animations: 'disabled' })
 expect((await request.head('/v1/observer/internal-events/browser-accepted')).status()).toBe(200)
 expect((await request.post('/v1/observer/internal-events/browser-accepted', { data: {} })).status()).toBe(405)
 expect(methods.length).toBeGreaterThan(0)
 expect(methods.every(method => method === 'GET')).toBeTruthy()
})
