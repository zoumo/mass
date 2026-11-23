import { expect, test } from '@playwright/test'

test.use({
 baseURL: `http://127.0.0.1:${process.env.SUPERAGENT_ROUTING_GATEWAY_PORT || '18894'}`,
 viewport: { width: 1280, height: 1060 },
})

test('Outbox detail renders Markdown and ordered options', async ({ page }) => {
 const imageRequests: string[] = []
 page.on('request', request => {
  if (request.url().includes('outbox-image.png')) imageRequests.push(request.url())
 })
 await page.goto('/superagent/?view=outbox')
 const dialog = page.getByRole('dialog')
 const content = dialog.getByTestId('structured-content')
 await page.getByRole('button', { name: '**Formatted reply**', exact: true }).click()
 await expect(content.locator('strong')).toHaveText('Formatted reply')
 await dialog.getByRole('button', { name: '关闭' }).click()
 await page.getByRole('button', { name: /^\*\*Choose what to do\.\*\*/ }).click()
 await expect(content.locator('strong')).toHaveText('Choose what to do.')
 await expect(content.getByRole('listitem')).toHaveText(['Pause', 'Continue'])
 await expect(content.getByRole('img', { name: 'Do not fetch' })).toHaveCount(0)
 await expect(content.locator('[data-marker="outbox-raw-html"]')).toHaveCount(0)
 expect(imageRequests).toEqual([])
})

test('Message and Thread link to read-only reply history and channel evidence', async ({ page, request }, testInfo) => {
 const methods = new Set<string>()
 page.on('request', r => {
  if (r.url().includes('/v1/')) {
   methods.add(r.method())
   expect(r.headers()['authorization']).toBeUndefined()
  }
 })
 await page.goto('/superagent/?view=messages&mode=records')
 const dialog = page.getByRole('dialog')
 await page.getByRole('button', { name: 'Original input outbox-observer-source', exact: true }).click()
 await dialog.getByRole('button', { name: '查看消息回执' }).click()
 await expect(page).toHaveURL(/view=outbox&messageId=mass-m[1-9]\d*(?:&|$)/)
 const table = page.getByRole('table')
 for (const status of ['待发送', '失败', '已发送']) {
  await expect(table.getByRole('cell', { name: status, exact: true }).first()).toBeVisible()
 }
 await table.getByRole('button', { name: 'Observer reply task', exact: true }).click()
 await expect(dialog.getByText('observer-task-42', { exact: true })).toBeVisible()
 await expect(dialog.getByText('渠道消息', { exact: true })).toHaveCount(0)
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
 await expect(dialog.getByText('observer_permanent_failure', { exact: true })).toBeVisible()
 await dialog.getByRole('button', { name: '关闭' }).click()
 await page.getByRole('navigation').getByRole('button', { name: '回执', exact: true }).click()
 await table.getByRole('button', { name: 'Observer reply sent', exact: true }).click()
 await expect(dialog.getByText('observer-send-task', { exact: true })).toBeVisible()
 await expect(dialog).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('outbox-sent.png'), fullPage: true, animations: 'disabled' })
 expect(methods).toEqual(new Set(['GET']))
 expect((await request.head('/v1/core/outbox-messages')).status()).toBe(200)
 expect((await request.post('/v1/core/outbox-messages', { data: {} })).status()).toBe(405)
})
