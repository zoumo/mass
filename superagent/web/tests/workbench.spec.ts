import { expect, test } from '@playwright/test'

const routingURL = `http://127.0.0.1:${process.env.SUPERAGENT_ROUTING_GATEWAY_PORT || '18894'}`

test('Topic, Issue and Trigger workspaces preserve cross-view links and explicit acceptance', async ({
  page,
  request,
}, testInfo) => {
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  page.on('request', (r) => {
    if (r.url().includes('/v1/')) {
      expect(r.method()).toBe('GET')
      expect(r.headers()['authorization']).toBeUndefined()
    }
  })
  const { items: topics } = await (await request.get(routingURL + '/v1/core/topics')).json()
  const { id: topicID } = topics.find((record: { title: string }) => record.title === 'Observer routing Topic')
  const otherTopic = topics.find((record: { id: string }) => record.id !== topicID).id
  const topicURL = routingURL + '/superagent/?view=topics&id=' + topicID
  await page.goto(topicURL)
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-CN')
  const topic = page.getByRole('article', { name: '当前 Topic' })
  const nav = page.getByRole('navigation', { name: '观察视角' })
  await expect(nav.getByRole('link', { name: 'Topic', exact: true })).toHaveAttribute('aria-current', 'page')
  await topic.getByRole('link', { name: '消息', exact: true }).click()
  const returned = topic.locator('li[data-kind="route_request"]')
  await returned.locator('summary').click()
  await expect(returned).toContainText('Observer return reason')
  await expect(returned).toContainText('Session 已受理')
  await returned.getByRole('link', { name: '查看此消息' }).click()
  const message = page.getByRole('article', { name: '当前消息' })
  await message.getByText('查看全文', { exact: true }).click()
  await expect(message.getByTestId('message-body')).toHaveText('Original input observer-routing')
  await message.getByRole('button', { name: 'Router', exact: true }).first().click()
  await expect(message.getByRole('region', { name: '路径详情' })).toContainText('已忽略 · 原因未记录')
  await expect(message.getByText('当前轮次').locator('xpath=following-sibling::dd[1]')).toHaveText('2')
  await expect(page.getByRole('textbox', { name: '按线程 ID 筛选' })).toHaveValue(/^dws:/)
  await page.goBack()
  await topic.getByRole('link', { name: '所属 Issue' }).click()
  await page
    .getByRole('complementary')
    .getByRole('link', { name: /Verify sample build/ })
    .click()
  const issue = page.getByRole('article', { name: '当前 Issue' })
  await expect(issue).toContainText('Run the sample build and inspect its exit code.')
  const profile = issue.getByRole('region', { name: '执行配置' })
  await expect(profile.locator('dd')).toHaveText(['agent', 'issue-model', 'medium'])
  await issue.getByRole('button', { name: 'Issue Session 提交报告 · Sample build verified' }).click()
  await expect(issue.getByRole('link', { name: '查看提交报告的会话' })).toHaveAttribute('href', /view=session-bindings&id=/)
  const issueID = new URL(page.url()).searchParams.get('id')
  await page.goto(routingURL + '/superagent/?view=issues&id=' + issueID)
  await page.reload()
  await expect(nav.getByRole('link', { name: 'Issue' })).toHaveAttribute('aria-current', 'page')
  await issue.getByRole('button', { name: 'Topic 已验收' }).click()
  await expect(issue).toContainText('Reviewed the sample build log')
  await expect(issue).toContainText('artifacts/build.log')
  await issue.getByRole('link', { name: 'Observer routing Topic', exact:true }).click()
  await topic.getByRole('link', { name: '查看 Session' }).click()
  const binding = page.getByRole('dialog')
  await expect(binding).toContainText('MASS Session')
  await expect(binding.getByText('运行时', { exact: true }).locator('xpath=following-sibling::dd[1]')).toHaveText('agent')
  await expect(binding.getByText('模型', { exact: true }).locator('xpath=following-sibling::dd[1]')).toHaveText('topic-model')
  await expect(binding.getByText('推理强度', { exact: true })).toHaveCount(0)
  expect(errors).toEqual([])
  const topicEvents = `${routingURL}/v1/core/topics/${topicID}/activity?filter=message`
  const first = await (await request.get(`${topicEvents}&limit=1&order=newest`)).json()
  expect(first.items.map((row: { kind: string }) => row.kind)).toEqual(['routed_message'])
  const cursor = encodeURIComponent(first.nextCursor)
  const next = await (await request.get(`${topicEvents}&limit=1&order=newest&cursor=${cursor}`)).json()
  expect(next.items.map((row: { kind: string }) => row.kind)).toEqual(['routed_message'])
  const scheduledMessage = await (
    await request.get(`${routingURL}/v1/core/trigger-messages/${next.items[0].messageId}`)
  ).json()
  expect(scheduledMessage.source).toBe('core:scheduled-task')
  expect(scheduledMessage.content).toEqual([{ type: 'text', text: 'Review the archived build.' }])
  expect(scheduledMessage.eventId).toBe(next.items[0].eventId)
  expect(next.nextCursor).toBeTruthy()
  const third = await (
    await request.get(`${topicEvents}&limit=1&order=newest&cursor=${encodeURIComponent(next.nextCursor)}`)
  ).json()
  expect(third.items.map((row: { kind: string }) => row.kind)).toEqual(['route_request'])
  expect((await request.get(`${topicEvents}&limit=1&order=oldest&cursor=${cursor}`)).status()).toBe(400)
  expect(
    (
      await request.get(
        `${routingURL}/v1/core/topics/${otherTopic}/activity?filter=message&order=newest&cursor=${cursor}`,
      )
    ).status(),
  ).toBe(400)
})

test('Narrow Trigger view shows the original and supports pagination without page overflow', async ({
  page,
  request,
}, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const {
    items: [original],
  } = await (await request.get('/v1/core/trigger-messages?limit=1')).json()
  await page.goto(`/superagent/?view=messages&id=${original.id}`)
  const message = page.getByRole('article', { name: '当前消息' })
  await expect(message.getByTestId('message-body')).toContainText('Long original text '.repeat(1500))
  await message.getByText('查看全文', { exact: true }).click()
  await expect(message.getByTestId('message-body')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
  await page.screenshot({ path: testInfo.outputPath('trigger-narrow.png'), animations: 'disabled' })
  await page.getByRole('link', { name: '返回消息列表' }).click()
  await expect(page.getByText('已显示 50 条消息')).toBeVisible()
  await page.getByRole('button', { name: '加载更多' }).click()
  await expect(page.getByRole('button', { name: '加载更多' })).toHaveCount(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
  await page.getByRole('link', { name: 'Issue', exact: true }).click()
  await expect(page.getByText('暂无 Issue 记录', { exact: true })).toBeVisible()
})
