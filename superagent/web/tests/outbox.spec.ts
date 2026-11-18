import { expect, test } from '@playwright/test'

test.use({
 baseURL: `http://127.0.0.1:${process.env.SUPERAGENT_ROUTING_BROWSER_PORT || '18794'}`,
 viewport: { width: 1280, height: 1060 },
})

test('Outbound echo links to the matched attempt in read-only reply history', async ({ page }) => {
 await page.goto('/observer/?view=messages&mode=records')
 await page.getByRole('button', { name: 'Original input observer-actual-message', exact: true }).click()
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByRole('heading', { name: '自身回声', exact: true })).toBeVisible()
 await expect(dialog.getByRole('button', { name: /^(放行|路由事件)$/ })).toHaveCount(0)
 await dialog.getByRole('button', { name: /^send_/ }).click()
 await expect(dialog.getByText('observer-actual-message', { exact: true })).toBeVisible()
 await expect(dialog.getByText('actual:conversation', { exact: true })).toBeVisible()
})

test('Message and Thread link to read-only reply history and channel evidence', async ({ page, request }, testInfo) => {
 const methods = new Set<string>()
 page.on('request', r => {
  if (r.url().includes('/v1/')) {
   methods.add(r.method())
   expect(r.headers()['authorization']).toBeUndefined()
  }
 })
 await page.goto('/observer/?view=messages&mode=records')
 const dialog = page.getByRole('dialog')
 await page.getByRole('button', { name: 'Original input outbox-observer-source', exact: true }).click()
 await dialog.getByRole('button', { name: '查看消息回执' }).click()
 await expect(page).toHaveURL(/view=outbox&messageId=msg_/)
 const table = page.getByRole('table')
 for (const status of ['待处理', '发送中', '未知', '失败', '已发送']) {
  await expect(table.getByRole('cell', { name: status, exact: true })).toBeVisible()
 }
 await table.getByRole('button', { name: 'Observer reply task', exact: true }).click()
 await expect(dialog.getByText(/^observer-task-42 · /)).toBeVisible()
 await expect(dialog.getByText(/发送 · 租约到期/)).toBeVisible()
 await expect(dialog.getByText('渠道发送结果中的消息', { exact: true })).toHaveCount(0)
 await dialog.getByRole('button', { name: '查看目标消息' }).click()
 await dialog.getByRole('button', { name: '查看线程 dws:outbox-observer-conversation', exact: true }).click()
 await dialog.getByRole('button', { name: '查看线程回执' }).click()
 await expect(page).toHaveURL(/view=outbox&threadKey=/)
 await table.getByRole('button', { name: 'Observer reply unknown', exact: true }).click()
 await expect(dialog.getByText(/恢复时可能重复发送/)).toBeVisible()
 await expect(dialog.getByText('允许重试时间', { exact: true })).toBeVisible()
 await expect(dialog.getByRole('button', { name: /^(重试|取消|恢复)$/ })).toHaveCount(0)
 await dialog.getByRole('button', { name: '关闭' }).click()
 await table.getByRole('button', { name: 'Observer reply permanent_failure', exact: true }).click()
 await dialog.getByRole('button', { name: '查看失败事件' }).click()
 await expect(dialog.getByRole('heading', { name: 'outbox.failed' })).toBeVisible()
 await dialog.getByRole('button', { name: '关闭' }).click()
 await page.getByRole('navigation').getByRole('button', { name: '回执', exact: true }).click()
 await table.getByRole('button', { name: 'Observer reply sent', exact: true }).click()
 await expect(dialog.getByText('observer-actual-message', { exact: true })).toBeVisible()
 await expect(dialog.getByText('actual:conversation', { exact: true })).toBeVisible()
 await expect(dialog).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('outbox-sent.png'), fullPage: true, animations: 'disabled' })
 expect(methods).toEqual(new Set(['GET']))
 expect((await request.head('/v1/observer/outbox')).status()).toBe(200)
 expect((await request.post('/v1/observer/outbox', { data: {} })).status()).toBe(405)
})
