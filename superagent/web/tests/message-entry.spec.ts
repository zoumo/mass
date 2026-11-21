import { test, expect, type APIRequestContext } from '@playwright/test'

const subject = '发布前检查：请确认本次发布涉及的配置变更。'
const body = subject + '\n' + '服务配置已更新，请核对本次发布的配置项和回归结果。'.repeat(120) + '\n<script>window.injected = true</script>'
async function seed(request: APIRequestContext) {
  const { items: [source] } = await (await request.get('/v1/observer/messages?limit=1')).json()
  const response = await request.post('/v1/channel/messages', {
    headers: { Authorization: 'Bearer ' + 'a'.repeat(48) },
    data: { operationId: 'observer-entry-chinese', message: {
      source: source.source, accountId: source.accountId, addressed: source.addressed, actor: '发布值班',
      threadRef: { ...source.threadRef, id: 'observer-entry' }, sourceMessageId: 'observer-entry-chinese', text: body,
    } },
  })
  expect(response.ok()).toBeTruthy()
  const receipt = await response.json()
  return (await request.get('/v1/observer/messages/' + receipt.messageId)).json()
}

test('Message entry reads the selected record, its full text and Core receipt', async ({ page, request, context }, testInfo) => {
  const record = await seed(request)
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const methods: string[] = []
  page.on('request', req => {
    if (req.url().includes('/v1/')) {
      expect(new URL(req.url()).pathname).toMatch(/^\/v1\/observer\//)
      methods.push(req.method())
    }
  })
  await page.setViewportSize({ width: 1360, height: 900 })
  await page.goto('/observer/?perspective=message')
  await expect(page.getByRole('navigation', { name: '观察视角' }).getByRole('link', { name: 'Message' })).toHaveAttribute('aria-current', 'page')
  const list = page.getByRole('complementary', { name: '最近消息' })
  await list.getByRole('link', { name: /发布前检查/ }).click()
  await expect(page).toHaveURL(new RegExp(record.id))
  const article = page.getByRole('article', { name: '当前消息' })
  await expect(article.getByRole('heading', { name: /发布前检查/ })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('message-desktop.png'), animations: 'disabled' })
  await article.getByText('查看全文', { exact: true }).click()
  await expect(article.getByTestId('message-body')).toHaveText(body)
  expect(await page.evaluate(() => 'injected' in window)).toBe(false)
  await article.getByText('Core 接收记录', { exact: true }).click()
  await expect(article.getByText(record.id, { exact: true })).toBeVisible()
  await expect(article.getByText(record.receivedAt, { exact: true })).toBeVisible()
  await article.getByRole('button', { name: '复制链接' }).click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(page.url())
  await page.getByRole('button', { name: '暂停自动刷新' }).click()
  await expect(page.getByRole('button', { name: '恢复自动刷新' })).toBeVisible()
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await expect(article.getByTestId('message-body')).toHaveText(body)
  await expect(article.getByText(record.id, { exact: true })).toBeVisible()
  await context.setOffline(true)
  await expect(page.getByRole('alert')).toContainText('快照已过时')
  await expect(article.getByTestId('message-body')).toHaveText(body)
  const fresh = page.waitForResponse(r => new URL(r.url()).pathname.endsWith('/messages/' + record.id) && r.ok())
  await context.setOffline(false)
  await fresh
  await expect(page.getByRole('alert')).toHaveCount(0)
  await list.getByRole('button', { name: '按线程键筛选' }).click()
  await list.getByRole('textbox', { name: '按线程键筛选' }).fill(record.threadKey)
  await list.getByRole('button', { name: '筛选', exact: true }).click()
  await expect(list.getByRole('link', { name: /发布前检查/ })).toBeVisible()
  await expect(list.getByText('已显示 1 条消息', { exact: true })).toBeVisible()
  await list.getByRole('link', { name: '清除筛选' }).click()
  await list.getByRole('button', { name: '加载更多' }).click()
  await expect(list.getByRole('button', { name: '加载更多' })).toHaveCount(0)
  const last = list.locator('.message-row').last()
  await last.click()
  const href = page.url()
  const position = await list.locator('.object-scroll').evaluate(el => el.scrollTop)
  await page.getByRole('link', { name: 'Topic', exact:true }).click()
  await expect(page.getByRole('heading', { name: 'Topic', exact:true })).toBeVisible()
  await page.goBack()
  await expect(page).toHaveURL(href)
  await expect(last).toHaveAttribute('aria-current', 'true')
  await expect.poll(() => list.locator('.object-scroll').evaluate(el => el.scrollTop)).toBe(position)
  await page.reload()
  await expect(last).toHaveAttribute('aria-current', 'true')
  await page.getByRole('link', { name: '诊断', exact: true }).click()
  await expect(page.getByRole('button', { name: '内部事件', exact: true })).toBeVisible()
  expect(new Set(methods)).toEqual(new Set(['GET']))
})

test('narrow Message entry opens an individual record and returns to its list', async ({ page, request }, testInfo) => {
  const record = await seed(request)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/observer/?perspective=message')
  const list = page.getByRole('complementary', { name: '最近消息' })
  await expect(list).toBeVisible()
  const selected = list.getByRole('link', { name: /发布前检查/ })
  await selected.focus()
  await page.keyboard.press('Enter')
  const article = page.getByRole('article', { name: '当前消息' })
  await expect(article.getByRole('heading', { name: /发布前检查/ })).toBeFocused()
  await expect(list).toBeHidden()
  await expect(page).toHaveURL(new RegExp(record.id))
  await page.screenshot({ path: testInfo.outputPath('message-narrow.png'), animations: 'disabled' })
  await article.getByText('查看全文', { exact: true }).click()
  await expect(article.getByTestId('message-body')).toHaveText(body)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
  await page.getByRole('link', { name: '返回消息列表' }).click()
  await expect(list).toBeVisible()
  await expect(article).toBeHidden()
  await expect(selected).toBeFocused()
  const second = list.locator('.message-row').nth(1)
  await second.click()
  await page.goBack()
  await expect(second).toBeFocused()
  await expect(second).toHaveAttribute('aria-current', 'true')
})
