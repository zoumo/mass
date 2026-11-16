import { expect, test, type Page } from '@playwright/test'

async function expectNoIntegrationControls(page: Page) {
  expect((await page.locator('th, label, button, [role="combobox"], [role="tab"]').allTextContents()).join('\n')).not.toMatch(/multica/i)
  await expect(page.locator('a[href*="multica" i]')).toHaveCount(0)
}

async function expectNoMetadata(page: Page) {
  await expect(page.locator('body')).not.toContainText(/external-workspace-marker|external-project-marker|external-issue-marker|external-status-marker|external-comment-marker/i)
  await expectNoIntegrationControls(page)
}

test('obsolete integration URL filters and their cursor do not change Ticket results or requests', async ({ page }) => {
  const queries: URLSearchParams[] = []
  page.on('request', request => {
    const url = new URL(request.url())
    if (url.pathname === '/observer/api/tickets') queries.push(url.searchParams)
  })
  const listing = page.getByRole('region', { name: 'Ticket 列表' })
  await page.goto('/observer/tickets?project=alpha&refresh=paused')
  await expect(listing.getByRole('link', { name: '责任核对主工作', exact: true })).toBeVisible()
  const titles = await listing.locator('tbody .ticket-title').allTextContents()
  await page.goto('/observer/tickets?project=alpha&refresh=paused&status=in_progress&multicaStatus=done&multicaProject=old&cursor=obsolete-filter-cursor')
  await expect(listing.getByRole('link', { name: '责任核对主工作', exact: true })).toBeVisible()
  expect(await listing.locator('tbody .ticket-title').allTextContents()).toEqual(titles)
  expect(new URL(page.url()).searchParams.has('status')).toBe(false)
  expect(new URL(page.url()).searchParams.has('cursor')).toBe(false)
  for (const query of queries) {
    expect([...query.keys()].some(key => /status|multica/i.test(key))).toBe(false)
    expect(query.get('cursor')).not.toBe('obsolete-filter-cursor')
  }
  expect(queries.length).toBeGreaterThanOrEqual(2)
  await page.getByRole('combobox', { name: '业务状态', exact: true }).click()
  await page.getByRole('option', { name: '已阻塞', exact: true }).click()
  await page.getByRole('button', { name: '应用筛选', exact: true }).click()
  await expect(page).toHaveURL(/state=blocked/)
  await expect(listing.getByRole('link', { name: '责任核对主工作', exact: true })).toBeVisible()
  await expect(listing.locator('tbody tr')).toHaveCount(1)
  await page.reload()
  await expect(listing.locator('tbody tr')).toHaveCount(1)
  await expectNoMetadata(page)
})

test('owned integration metadata is absent while Router, Ticket, Trigger and Output bodies remain intact', async ({ page, baseURL }) => {
  const objective = JSON.stringify({ text: 'Authoritative Multica objective', multica: { workspaceId: 'business-workspace' }, issue: { id: 'business-issue' } })
  const acceptance = 'Keep Multica acceptance unchanged.\nhttps://multica.example/acceptance'
  const content = JSON.stringify({ text: 'Authoritative Multica trigger', multica: { projectId: 'business-project' }, issue: { id: 'business-issue' } })
  const output = 'Authoritative Multica reply\nhttps://multica.example/reply\n{"multica":{"status":"business-value"}}'
  let instructions = ''
  const unsafe: string[] = []
  page.on('request', request => {
    if (new URL(request.url()).origin !== baseURL || !['GET', 'HEAD'].includes(request.method())) unsafe.push(request.url())
  })
  await page.route(/\/observer\/api\/(routers|tickets|triggers|outputs)(\/|\?)/, async route => {
    const response = await route.fetch()
    const data = await response.json()
    const inject = (record: Record<string, unknown>) => Object.assign(record, {
      multica: { workspaceId: 'external-workspace-marker', projectId: 'external-project-marker', issueId: 'external-issue-marker', url: 'https://multica.example/external-issue-marker' },
      issue: { id: 'external-issue-marker', identifier: 'external-issue-marker' }, multicaStatus: 'external-status-marker',
    })
    if (data.items) {
      data.items.forEach(inject)
      if (new URL(route.request().url()).pathname.endsWith('/outputs')) data.items.forEach((record: Record<string, unknown>) => { record.summary = output })
    } else {
      inject(data)
      data.reports?.forEach((report: Record<string, unknown>) => {
        report.commentId = 'external-comment-marker'
        report.status = 'external-status-marker'
        report.errorReason = 'external-status-marker'
      })
      if (data.instructions) instructions = data.instructions
      if (data.objective) { data.objective = objective; data.acceptance = acceptance }
      if (data.content) data.content = content
    }
    await route.fulfill({ response, json: data })
  })
  await page.goto('/observer/routers?project=alpha&selected=alpha&refresh=paused')
  await expect(page.getByRole('heading', { name: 'Alpha Router', exact: true })).toBeVisible()
  await page.getByRole('tab', { name: '处理配置', exact: true }).click()
  await page.getByText('Router 任务指引', { exact: true }).click()
  await expect.poll(() => instructions).toContain('Inspect work safely')
  expect(await page.locator('pre').textContent()).toBe(instructions)
  await expectNoMetadata(page)
  await page.goto('/observer/tickets?project=alpha&selected=t03-root&selectedProject=alpha&refresh=paused')
  const detail = page.getByRole('region', { name: 'Ticket 责任详情' })
  await expect(detail.getByRole('heading', { name: '责任核对主工作' })).toBeVisible()
  await detail.getByText('展开目标（文本）', { exact: true }).click()
  expect(await detail.locator('pre').nth(0).textContent()).toBe(objective)
  await detail.getByText('展开验收（文本）', { exact: true }).click()
  expect(await detail.locator('pre').nth(1).textContent()).toBe(acceptance)
  await detail.getByText('技术身份与原始字段', { exact: true }).click()
  await expect(detail).toContainText('t03-session-new')
  await expect(detail).toContainText('alpha')
  await expectNoMetadata(page)
  await detail.getByRole('tab', { name: '派工与委派', exact: true }).click()
  for (const summary of await detail.locator('.ticket-process details > summary').all()) await summary.click()
  await expectNoMetadata(page)
  await page.goto('/observer/triggers?project=alpha&refresh=paused')
  await page.getByRole('link', { name: 'Router 后交 Worker', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Router 后交 Worker', exact: true })).toBeVisible()
  await expect(page.getByRole('dialog')).toContainText('Authoritative Multica trigger')
  await page.getByText('技术详情与原始内容（文本）', { exact: true }).click()
  expect(await page.getByRole('dialog').locator('pre').textContent()).toBe(content)
  await expectNoMetadata(page)
  await page.goto('/observer/triggers?project=alpha&selected=output-origin&detailTab=outputs&refresh=paused')
  const reply = page.getByRole('region', { name: '显式回复', exact: true }).locator('pre.output-content').first()
  await expect(reply).toBeVisible()
  expect(await reply.textContent()).toBe(output)
  await expectNoMetadata(page)
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: '使用说明', exact: true }).click()
  await expect(page.getByRole('heading', { name: '使用说明', exact: true })).toBeVisible()
  await expectNoMetadata(page)
  expect(unsafe).toEqual([])
})

test('Session history and live raw records preserve integration mentions, nested fields and serialized tool JSON', async ({ page }) => {
  const text = 'Authoritative Multica tool result\nhttps://multica.example/result'
  const payload = {
    toolName: 'inspect', status: 'completed', projectId: 'alpha', sessionId: 'session-old',
    multica: { workspaceId: 'source-workspace', url: 'https://multica.example/project' },
    nested: [{ issue: { id: 'source-issue' }, multica_status: 'source-status', commentId: 'source-comment', ticketId: 'ticket-a' }],
    rawInput: JSON.stringify({ multicaConfig: { workspaceId: 'source-workspace' }, issue_id: 'source-issue', projectId: 'alpha', sessionId: 'session-old', value: 'safe input' }),
    rawOutput: JSON.stringify({ url: 'https://multica.example/issue', value: 'safe output' }),
  }
  await page.addInitScript(() => {
    const Native = window.EventSource
    window.EventSource = class extends Native {
      constructor(url: string | URL, config?: EventSourceInit) {
        super(url, config)
        ;(window as unknown as { t11Source: EventSource }).t11Source = this
      }
    } as typeof EventSource
  })
  await page.goto('/observer/resources?project=alpha&selected=session-old&detailTab=history&refresh=paused')
  await expect(page.getByText('实时观察 · 持久历史已补读', { exact: true })).toBeVisible()
  for (const preview of [true, false]) {
    await page.evaluate(({ preview, text, payload }) => {
      const event = { eventId: 't11-event', sessionId: 'session-old', type: 'agent.tool_result', time: '2026-11-16T01:00:00Z', preview, text, payload }
      const data = preview ? event : { events: [event], earlierCursors: [], observedAt: '2026-11-16T01:00:00Z' }
      ;(window as unknown as { t11Source: EventSource }).t11Source.dispatchEvent(new MessageEvent(preview ? 'preview' : 'history', { data: JSON.stringify(data) }))
    }, { preview, text, payload })
    const record = page.locator('.session-events > li').filter({ hasText: 'Authoritative Multica tool result' })
    await expect(record).toHaveCount(1)
    const outer = record.locator(':scope > details')
    if (await outer.getAttribute('open') === null) await outer.locator(':scope > summary').click()
    expect(await record.locator('pre.resource-event-text').textContent()).toBe(text)
    const raw = record.locator('.resource-raw-event')
    if (await raw.getAttribute('open') === null) await raw.locator(':scope > summary').click()
    const displayedPayload = preview ? { ...payload, previewEventIds: ['t11-event'] } : payload
    expect(await raw.locator('pre').textContent()).toBe(JSON.stringify(displayedPayload, null, 2))
    await expect(record.getByText(preview ? '实时预览，未确认持久' : '持久事实', { exact: true })).toBeVisible()
    await expectNoIntegrationControls(page)
  }
  await page.screenshot({ path: 'test-results/t11-session-original-record.png' })
})
