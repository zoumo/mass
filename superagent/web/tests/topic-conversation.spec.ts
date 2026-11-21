import { expect, test } from '@playwright/test'

test.use({ baseURL: `http://127.0.0.1:${process.env.SUPERAGENT_ROUTING_BROWSER_PORT || '18794'}` })

const observer = '/observer/?perspective=topic-conversation'
const audit = `http://127.0.0.1:${process.env.SUPERAGENT_OBSERVER_AUDIT_PORT || '18795'}`
const routed = 'Original input observer-routing'
const direct = 'Synthetic PR review arrived.'

test('reads distinct Router and Core ingress with their own Thread and source links', async ({ page }) => {
  await page.goto('/observer/?perspective=message')
  await page.getByRole('link', { name: 'Topic 会话', exact: true }).click()
  const topics = page.getByRole('complementary', { name: 'Topics', exact: true })
  const activeTopic = topics.getByRole('link', { name: /Observer routing Topic/ })
  await expect(topics.getByRole('link').first()).toHaveText(/Observer routing Topic/)
  await expect(activeTopic).toHaveAttribute('aria-current', 'true')

  const timeline = page.getByRole('region', { name: '群聊时间线' })
  const filters = timeline.getByRole('navigation')
  await filters.getByRole('link', { name: 'Router 路由入群' }).click()
  const router = timeline.getByRole('article').filter({ hasText: routed })
  const routerAvatar = router.getByRole('group', { name: '发送方' }).getByRole('link', { name: /查看 Router/ })
  await expect(routerAvatar).toHaveAttribute('href', /perspective=router&id=/)
  await expect(router.getByRole('group', { name: '收件方' }).getByRole('link', { name: /查看 Topic/ })).toHaveAttribute('href', /perspective=topic&id=/)
  await expect(router.getByRole('link', { name: /查看 Thread · dws:/ })).toHaveAttribute('href', /view=threads/)
  await expect(timeline.getByText(direct)).toHaveCount(0)
  await router.getByRole('link', { name: new RegExp(routed) }).click()
  const details = page.getByRole('complementary', { name: '消息详情' })
  await expect(details.getByText(routed, { exact: true })).toBeVisible()
  await expect(details.getByText('independent requested work', { exact: true })).toBeVisible()
  await expect(details.getByRole('link', { name: '查看原始消息 →' })).toHaveAttribute('href', /perspective=message&id=/)
  await expect(details.getByRole('link', { name: '查看事件记录 →' })).toHaveAttribute('href', /view=routing-events&id=/)
  await page.reload()
  await expect(details.getByText('independent requested work', { exact: true })).toBeVisible()

  await filters.getByRole('link', { name: 'Core Thread 直投' }).click()
  const core = timeline.getByRole('article').filter({ hasText: direct })
  await expect(core.getByText('Core', { exact: true })).toBeVisible()
  const prThread = core.getByRole('link', { name: /github\.com\/example\/alpha\/pull\/42/ })
  await expect(timeline.getByText(routed)).toHaveCount(0)
  await prThread.click()
  await expect(page).toHaveURL(/view=threads&id=/)
  await expect(page.getByRole('dialog')).toBeVisible()
})

test('search follows browser history and a narrow viewport reveals the selected detail', async ({ page }) => {
  await page.setViewportSize({ width: 420, height: 760 })
  await page.goto(observer)
  const topics = page.getByRole('complementary', { name: 'Topics', exact: true })
  const search = topics.getByRole('searchbox', { name: '搜索 Topic' })
  await search.fill('archived')
  await search.press('Enter')
  await expect(topics.getByRole('link')).toHaveCount(1)
  await expect(topics.getByRole('link').first()).toHaveText(/Archived sample Topic/)
  await page.goBack()
  await expect(search).toHaveValue('')
  await page.goForward()
  await expect(search).toHaveValue('archived')
  const timeline = page.getByRole('region', { name: '群聊时间线' })
  await timeline.getByRole('link', { name: /Original input observer-finished/ }).click()
  const details = page.getByRole('complementary', { name: '消息详情' })
  await expect(details).toBeFocused()
  await expect(details.getByRole('heading', { name: '消息详情' })).toBeInViewport()
})

test('reads Issue creation and Issue Session dispatch separately, with real group records and delivery references', async ({ page }) => {
  await page.goto(observer)
  const timeline = page.getByRole('region', { name: '群聊时间线' })
  await expect(timeline.getByRole('heading', { name: 'Observer routing Topic' })).toBeVisible()
  const group = page.getByRole('complementary', { name: '群组信息' })
  await expect(group.getByRole('link', { name: /^dws:/ })).toBeVisible()
  await expect(group.getByRole('link', { name: /github\.com\/example\/alpha\/pull\/42/ })).toBeVisible()
  await expect(group.getByRole('link', { name: '查看 Issue · Verify sample build' })).toHaveAttribute('href', /perspective=issue&id=/)
  await expect(group.getByRole('link', { name: '查看 Issue · Review blocked build' })).toBeVisible()

  const filters = timeline.getByRole('navigation')
  await filters.getByRole('link', { name: '创建 Issue', exact: true }).click()
  const created = timeline.getByRole('article').filter({ hasText: 'Verify sample build' })
  await expect(created.locator('header')).toContainText('创建 Issue')
  await expect(created.locator('header code')).toHaveText('mass-t1-i1')
  await expect(created.getByRole('group', { name: '收件方' }).getByRole('link')).toHaveAttribute('href', /perspective=issue&id=mass-t1-i1/)
  await created.getByRole('link', { name: /创建 Issue · Verify sample build/ }).click()
  const details = page.getByRole('complementary', { name: '消息详情' })
  await expect(details.getByRole('link', { name: '查看 Issue →' })).toHaveAttribute('href', /perspective=issue&id=/)

  await filters.getByRole('link', { name: '任务派发' }).click()
  await expect(timeline.getByText(direct)).toHaveCount(0)
  const startup = timeline.getByRole('article').filter({ hasText: 'Verify sample build' })
  await expect(startup.getByRole('group', { name: '发送方' })).toHaveText('Topic Session')
  await expect(startup.getByRole('group', { name: '收件方' })).toHaveText('Issue Session')
  await expect(startup.locator('header code')).toHaveText(/^evt_/)
  await startup.getByRole('link', { name: /启动 Issue Session/ }).click()
  await expect(details.getByText('Session 已受理', { exact: true })).toBeVisible()
  await expect(details.getByText('artifacts/build-notes.md', { exact: true })).toBeVisible()
  await expect(details.getByRole('link', { name: '查看 SessionBinding →' })).toHaveAttribute('href', /view=session-bindings&id=/)
  await expect(details.getByRole('link', { name: '查看事件记录 →' })).toHaveAttribute('href', /view=internal-events&id=/)

  await timeline.getByRole('link', { name: /目标更新/ }).click()
  await expect(details.getByText('目标修订', { exact: true })).toBeVisible()
})

test('renders Markdown and previews a structured choice directly in the conversation', async ({ page }) => {
  await page.goto(observer)
  const timeline = page.getByRole('region', { name: '群聊时间线' })
  const checklist = timeline.getByTestId('structured-content').filter({ hasText: 'Archive review checklist' })
  await expect(checklist.getByRole('heading', { name: 'Archive review checklist' })).toBeVisible()
  await expect(checklist.locator('strong')).toHaveText('current build')
  await expect(checklist.getByRole('listitem')).toHaveText(['Check the build log', 'Compare the manifest with the previous build'])
  await expect(checklist.locator('code')).toHaveText('make verify')

  const choices = timeline.getByTestId('structured-content').filter({ hasText: 'Which archive should we review?' })
  await expect(choices.locator('strong')).toHaveText('Which archive should we review?')
  const current = choices.getByRole('button', { name: 'Current build' })
  const previous = choices.getByRole('button', { name: 'Previous build' })
  await expect(current).toBeVisible()
  await previous.click()
  await expect(previous).toHaveAttribute('aria-pressed', 'true')
  await expect(current).toHaveAttribute('aria-pressed', 'false')
  await expect(choices.locator('output code')).toHaveText('{"optionId":"previous"}')
  await expect(page.getByRole('complementary', { name: '群组信息' })).toBeVisible()
})

test('defaults to Topic conversations and refreshes Report order while preserving selection and reading position', async ({ page, context, request }, testInfo) => {
  const fixture = await request.get(audit + '/topic-conversation-fixture')
  expect(fixture.ok()).toBe(true)
  const { endpoint } = await fixture.json()
  await page.goto(endpoint + '/observer/')
  await expect(page.getByRole('heading', { name: 'Topic 会话', exact: true })).toBeVisible()
  const views = page.getByRole('navigation', { name: '观察视角' })
  for (const name of ['Message', 'Router', 'Topic', 'Issue']) {
    await expect(views.getByRole('link', { name, exact: true })).toHaveAttribute('href', `/observer/?perspective=${name.toLowerCase()}`)
  }
  await expect(page.getByRole('link', { name: '诊断', exact: true })).toBeVisible()
  const topics = page.getByRole('complementary', { name: 'Topics', exact: true })
  const otherTopic = topics.getByRole('link', { name: /Other recent conversation/ })
  const pipeline = topics.getByRole('link', { name: /Conversation pipeline/ })
  await expect(topics.getByRole('link').first()).toHaveText(/Other recent conversation/)
  await expect(otherTopic).toHaveAttribute('aria-current', 'true')

  const reading = await context.newPage()
  await reading.goto(endpoint + (await pipeline.getAttribute('href')))
  const timeline = reading.getByRole('region', { name: '群聊时间线' })
  const records = timeline.getByLabel('会话记录')
  const entries = records.locator('[data-event-id]')
  await expect(entries).toHaveCount(20)
  await records.evaluate(node => { node.scrollTop = 0 })
  const anchor = records.locator('[data-event-id]').filter({ hasText: 'Original input conversation-extra-02' })
  const anchorOffset = () => anchor.evaluate(node => {
    const viewport = node.closest('.conversation-scroll')!
    return node.getBoundingClientRect().top - viewport.getBoundingClientRect().top
  })
  const offset = await anchorOffset()
  await records.getByRole('button', { name: '加载更早记录' }).click()
  await expect(entries).toHaveCount(26)
  expect(Math.abs(await anchorOffset() - offset)).toBeLessThan(2)

  for (let attempt = 0; attempt < 2; attempt++) {
    const report = await request.post(audit + '/topic-conversation-fixture/report')
    expect(report.status()).toBe(204)
  }
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await expect(topics.getByRole('link').first()).toHaveText(/Conversation pipeline/)
  await expect(otherTopic).toHaveAttribute('aria-current', 'true')
  await reading.getByRole('button', { name: '刷新', exact: true }).click()
  await expect(entries).toHaveCount(27)
  expect(Math.abs(await anchorOffset() - offset)).toBeLessThan(2)
  await expect(reading.getByRole('link', { name: /Conversation pipeline/ })).toHaveAttribute('aria-current', 'true')

  const milestones = entries.filter({ hasText: /Original input conversation-routed|创建 Issue.*Verify conversation build|启动 Issue Session|Original input conversation-direct|Conversation build verified/ })
  await expect(milestones).toHaveText([
    /Router.+Original input conversation-routed/,
    /Topic.*Issue.*创建 Issue.*Verify conversation build/,
    /Topic.*Issue Session.*启动 Issue Session.*Verify conversation build/,
    /Core.+Original input conversation-direct/,
    /Issue Session.*Topic.*申请验收.*Conversation build verified/,
  ])
  await reading.getByRole('button', { name: /有新消息 · 跳到最新/ }).click()
  const report = timeline.getByRole('link', { name: /申请验收.+Conversation build verified/ })
  await expect(report).toBeInViewport()
  await report.click()
  const details = reading.getByRole('complementary', { name: '消息详情' })
  await expect(details.getByText('All conversation build checks passed.')).toBeVisible()
  await expect(details.getByText('artifacts/conversation.log')).toBeVisible()
  await expect(details.getByText('当前报告', { exact: true })).toBeVisible()
  await expect(details.getByText('待 Topic 验收')).toBeVisible()
  await expect(details.getByRole('link', { name: '查看 Issue →' })).toHaveAttribute('href', /perspective=issue&id=/)
  await expect(details.getByRole('link', { name: '查看 SessionBinding →' })).toHaveAttribute('href', /view=session-bindings&id=/)
  await expect(details.getByRole('link', { name: '查看 Report →' })).toHaveAttribute('href', /view=reports&id=/)
  await expect(details.getByRole('link', { name: '查看启动事件 →' })).toHaveAttribute('href', /view=internal-events&id=/)
  await reading.reload()
  await expect(details.getByText('artifacts/conversation.log')).toBeVisible()
  await reading.screenshot({ path: testInfo.outputPath('topic-conversation.png') })
  await reading.getByRole('link', { name: 'Message', exact: true }).click()
  await reading.getByRole('link', { name: 'Superagent 观察台首页' }).click()
  await expect(reading).toHaveURL(endpoint + '/observer/')
  await expect(reading.getByRole('heading', { name: 'Conversation pipeline' })).toBeVisible()
})

test('reads Issue acceptance from the saved completion alongside its Issue Report', async ({ page }) => {
  await page.goto('/observer/')
  const timeline = page.getByRole('region', { name: '群聊时间线' })
  await timeline.getByRole('link', { name: 'Issue Report', exact: true }).click()
  await expect(timeline.getByRole('article')).toHaveCount(2)
  await timeline.getByRole('link', { name: /Sample build verified/ }).click()
  const details = page.getByRole('complementary', { name: '消息详情' })
  await expect(details.getByText('Build exited with code 0.')).toBeVisible()
  await expect(details.getByText('历史报告', { exact: true })).toBeVisible()
  await expect(details.getByRole('heading', { name: 'Issue 已验收' })).toBeVisible()
  await expect(details.getByText('Reviewed the sample build log')).toBeVisible()
})

test('opens event Trace in the observer and returns to the conversation', async ({ page }) => {
  const traceId = 'db64663e60fb444b8a93ea9c06c24413'
  const start = '5b147a1058d5e09f'
  await page.route(url => url.pathname.endsWith('/topic-activity') && url.searchParams.has('topicId'), async route => {
    const response = await route.fetch()
    const result = await response.json()
    for (const event of result.items) event.traceAssociations = [{ traceId, phase: 'enqueue' }, { traceId, phase: 'dispatch' }]
    await route.fulfill({ response, json: result })
  })
  await page.route(url => url.pathname.endsWith('/traces/' + traceId), route => route.fulfill({ json: {
    traceId, startedAt: '2026-11-20T00:00:00Z', truncated: false, spans: [
      { id: start, name: 'root request', service: 'core', offsetUs: 0, durationUs: 4200, status: 0 },
      { id: '6a147a1058d5e09f', parentId: start, name: 'mcp handler', tool: 'update_issue', role: 'issue', service: 'core', offsetUs: 130, durationUs: 610, status: 0 },
    ],
  } }))
  await page.goto(observer + '&id=mass-t1')
  const card = page.getByRole('article').filter({ hasText: routed })
  const header = card.locator('header')
  await expect(header.locator('.conversation-card-type + .conversation-card-record-id')).toHaveText('mass-m1')
  const link = header.getByRole('link', { name: '查看 Trace', exact: true })
  await expect(link).toHaveText('Trace')
  await expect(link).toHaveAttribute('href', new RegExp(traceId))
  await expect(card).not.toContainText(traceId)
  await link.click()
  await expect(page).toHaveURL(/perspective=trace/)
  await expect(page.getByRole('region', { name: 'Trace 瀑布图' })).toBeVisible()
  await page.getByRole('button', { name: /update_issue/ }).click()
  const detail = page.getByRole('complementary', { name: 'Span 详情' })
  await expect(detail).toContainText('610 µs')
  await detail.getByRole('button', { name: '跳转父 Span' }).click()
  await expect(detail).toContainText('root request')
  await page.reload()
  await expect(page.getByText('2 spans', { exact: true })).toBeVisible()
  await page.goBack()
  await expect(link).toBeVisible()
})
