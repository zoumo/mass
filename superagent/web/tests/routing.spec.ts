import { test, expect } from '@playwright/test'

test.use({ baseURL: `http://127.0.0.1:${process.env.SUPERAGENT_ROUTING_GATEWAY_PORT || '18894'}` })

test('routing, preparation and returns are visible through read-only navigation', async ({ page, request }, testInfo) => {
 const methods: string[] = []
 const errors: string[] = []
 page.on('pageerror', e => errors.push(e.message))
 page.on('request', r => { if (r.url().includes('/v1/')) { methods.push(r.method()); expect(r.headers()['authorization']).toBeUndefined() } })
 await page.goto('/superagent/?view=topics&mode=records')
 await expect(page.getByText('核心服务已就绪', { exact: true })).toBeVisible()
 await page.getByRole('button', { name: 'Observer routing Topic', exact: true }).click()
 await expect(page.getByRole('dialog').getByText('进行中', { exact: true })).toBeVisible()
 await page.getByRole('button', { name: '查看 SessionBinding', exact: true }).click()
 await expect(page.getByRole('dialog').getByText('已就绪', { exact: true })).toBeVisible()
 await expect(page.getByRole('dialog').getByText(/topic-g1/).first()).toBeVisible()
 await expect(page.getByRole('dialog')).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('routing-preparation.png'), fullPage: true, animations: 'disabled' })
 await page.getByRole('button', { name: '关闭', exact: true }).click()
 await page.getByRole('button', { name: '消息', exact: true }).click()
 await page.getByRole('button', { name: 'Original input observer-routing', exact: true }).click()
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByRole('region', { name: '路由历史' })).toContainText('Observer return reason')
 await dialog.getByRole('button', { name: '查看路由事件' }).click()
 await expect(page.getByRole('table').getByText('Session 已受理').first()).toBeVisible()
 await page.getByRole('table').getByRole('button', { name: '路由请求', exact: true }).last().click()
 await expect(page.getByTestId('routing-record')).toContainText('"kind": "route_request"')
 await expect(page.getByTestId('routing-record')).toContainText('Observer return reason')
 await expect(page.getByTestId('routing-record')).toContainText('"returnedEventId": "evt_')
 await page.screenshot({ path: testInfo.outputPath('routing-return.png'), fullPage: true, animations: 'disabled' })
 for (const collection of ['topics', 'internal-events', 'session-bindings']) {
  expect((await request.get(`/v1/core/${collection}`)).status()).toBe(200)
  expect((await request.head(`/v1/core/${collection}`)).status()).toBe(200)
  expect((await request.post(`/v1/core/${collection}`, { data: {} })).status()).toBe(405)
 }
 expect(errors).toEqual([])
 expect(methods.every(method => method === 'GET')).toBeTruthy()
})

test('internal event shows Session acceptance and stable identity', async ({ page, request }, testInfo) => {
 const methods: string[] = []
 page.on('request', r => { if (r.url().includes('/v1/')) methods.push(r.method()) })
 await page.goto('/superagent/?view=internal-events&id=browser-accepted')
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByText('Session 已受理', { exact: true })).toBeVisible()
 await expect(page.getByTestId('routing-record')).toContainText('"event_id": "browser-accepted"')
 await expect(page.getByTestId('routing-record')).toContainText('"kind": "context_supplement"')
 await expect(dialog.getByRole('button', { name: /重试|替换|恢复|确认投递/ })).toHaveCount(0)
 await page.screenshot({ path: testInfo.outputPath('internal-event.png'), fullPage: true, animations: 'disabled' })
 expect((await request.head('/v1/core/internal-events/browser-accepted')).status()).toBe(200)
 expect((await request.post('/v1/core/internal-events/browser-accepted', { data: {} })).status()).toBe(405)
 expect(methods.length).toBeGreaterThan(0)
 expect(methods.every(method => method === 'GET')).toBeTruthy()
})
