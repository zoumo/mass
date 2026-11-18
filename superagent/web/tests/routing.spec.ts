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
 await expect(page.getByTestId('routing-record')).toContainText('"round": 2')
 await expect(page.getByRole('dialog')).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('routing-return.png'), fullPage: true, animations: 'disabled' })
 await page.getByRole('button', { name: '关闭', exact: true }).click()
 await page.getByRole('button', { name: '订阅', exact: true }).click()
 await expect(page.getByText('显示 4 条订阅', { exact: false })).toBeVisible()
 await page.getByRole('table').getByRole('button', { name: /^sub_/ }).first().click()
 await expect(page.getByRole('dialog').getByRole('button', { name: /^查看线程 dws:/ })).toBeVisible()
 await page.getByRole('button', { name: '关闭', exact: true }).click()
 await page.getByRole('button', { name: '路由决定', exact: true }).click()
 await expect(page.getByText('显示 3 条路由决定', { exact: false })).toBeVisible()
 for (const collection of ['topics', 'decisions', 'subscriptions', 'relations', 'bindings']) {
  expect((await request.get(`/v1/observer/${collection}`)).status()).toBe(200)
  expect((await request.head(`/v1/observer/${collection}`)).status()).toBe(200)
  expect((await request.post(`/v1/observer/${collection}`, { data: {} })).status()).toBe(405)
 }
 expect(errors).toEqual([])
 expect(methods.every(method => method === 'GET')).toBeTruthy()
})

test('delivery evidence, ACK and unresolved outcomes are visible without write controls', async ({ page, request }, testInfo) => {
 const requests: string[] = []
 page.on('request', r => {
  if (r.url().includes('/v1/')) {
   requests.push(r.method())
   expect(r.headers()['authorization']).toBeUndefined()
  }
 })
 await page.goto('/observer/?view=events&id=browser-ack')
 await expect(page.getByRole('dialog').getByText('已投递', { exact: true })).toBeVisible()
 await page.getByRole('button', { name: '查看投递记录', exact: true }).click()
 await expect(page.getByRole('dialog').getByText('确认回执')).toBeVisible()
 await expect(page.getByRole('dialog').getByText('等待确认回执')).toHaveCount(0)
 await expect(page.getByTestId('submission-receipt')).toContainText('"nativeAccepted": true')
 await page.getByRole('button', { name: '批次', exact: true }).click()
 await expect(page.getByRole('dialog').getByText('提交回执')).toBeVisible()
 await page.getByRole('button', { name: '最近一次尝试', exact: true }).click()
 await expect(page.getByTestId('submission-receipt')).toContainText('"sent": true')
 await page.goto('/observer/?view=events&id=browser-missing-evidence')
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByText('原生执行结果未确认', { exact: true })).toBeVisible()
 await expect(dialog.getByText('观测中断 · missing_submission_evidence', { exact: true })).toBeVisible()
 await page.getByRole('button', { name: '查看投递记录', exact: true }).click()
 await expect(dialog.getByText('需关注', { exact: true })).toBeVisible()
 await expect(dialog.getByText('之前的输入可能已经生效。')).toBeVisible()
 await expect(dialog.getByRole('button', { name: /重试|替换|恢复|确认投递/ })).toHaveCount(0)
 await expect(dialog).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('delivery-unknown.png'), fullPage: true, animations: 'disabled' })
 for (const collection of ['deliveries', 'batches', 'attempts', 'recoveries']) {
  expect((await request.head(`/v1/observer/${collection}`)).status()).toBe(200)
  expect((await request.post(`/v1/observer/${collection}`, { data: {} })).status()).toBe(405)
 }
 expect(requests.length).toBeGreaterThan(0)
 expect(requests.every(method => method === 'GET')).toBeTruthy()
})
