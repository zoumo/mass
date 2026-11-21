import { test, expect } from '@playwright/test'

test.use({
 baseURL: `http://127.0.0.1:${process.env.SUPERAGENT_ROUTING_BROWSER_PORT || '18794'}`,
 viewport: { width: 1280, height: 1060 },
})

test('Topic Timer shows the fired revision and followup delivery evidence', async ({ page, request }, testInfo) => {
 page.on('request', r => {
  if (r.url().includes('/v1/')) {
   expect(r.method()).toBe('GET')
   expect(r.headers()['authorization']).toBeUndefined()
  }
 })
 await page.goto('/observer/?view=topics&mode=records')
 await page.getByRole('button', { name: 'Observer routing Topic', exact: true }).click()
 await page.getByRole('button', { name: '查看 Topic 定时器', exact: true }).click()
 await expect(page).toHaveURL(/view=timers&topicId=/)
 await page.getByRole('button', { name: 'Review the archived build.', exact: true }).click()
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByText('定时器状态', { exact: true }).locator('xpath=following-sibling::dd[1]')).toHaveText('已触发')
 await expect(dialog.getByText('版本', { exact: true }).locator('xpath=following-sibling::dd[1]')).toHaveText('2')
 await expect(dialog.getByText('计划触发时间 (UTC)', { exact: true }).locator('xpath=following-sibling::dd[1]')).toHaveText('2020-01-02T00:00:00.012000000Z')
 await expect(dialog.getByText('实际触发时间 (UTC)', { exact: true }).locator('xpath=following-sibling::dd[1]')).toHaveText(/^\d{4}-.+Z$/)
 await expect(dialog.getByText('Archive checklist · artifacts/archive-checklist.md', { exact: true })).toBeVisible()
 await expect(dialog.getByText('Session 已受理', { exact: true })).toBeVisible()
 await expect(dialog.getByText('timer.followup', { exact: true })).toBeVisible()
 await expect(dialog.getByRole('button', { name: /^查看线程 core%3Atimer:/ })).toBeVisible()
 await expect(dialog.getByRole('button', { name: /^(设置|编辑|更新|取消|重新安排|重开)/ })).toHaveCount(0)
 await expect(dialog).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('timer-followup.png'), fullPage: true, animations: 'disabled' })
 const taskURL = page.url()
 await dialog.getByRole('button', { name: '查看跟进事件', exact: true }).click()
 await expect(dialog.getByText('Session 已受理', { exact: true })).toBeVisible()
 await expect(page).toHaveURL(/view=routing-events&id=evt_/)
 await page.goto(taskURL)
 await dialog.getByRole('button', { name: '查看触发消息', exact: true }).click()
 await expect(page).toHaveURL(/view=messages&id=mass-m[1-9]\d*(?:&|$)/)
 await expect(dialog.getByText('Review the archived build.', { exact: true })).toBeVisible()
 await dialog.getByRole('button', { name: /^查看线程 core%3Atimer:/ }).click()
 await expect(dialog.getByRole('button', { name: '查看线程订阅' })).toBeVisible()
 expect((await request.get('/v1/observer/timers')).status()).toBe(200)
 expect((await request.head('/v1/observer/timers')).status()).toBe(200)
 expect((await request.post('/v1/observer/timers', { data: {} })).status()).toBe(405)
})

test('Issue requirements, reports, input evidence and business acceptance are read only', async ({ page, request }, testInfo) => {
 const methods: string[] = []
 const errors: string[] = []
 page.on('pageerror', error => errors.push(error.message))
 page.on('request', r => {
  if (r.url().includes('/v1/')) {
   methods.push(r.method())
   expect(r.headers()['authorization']).toBeUndefined()
  }
 })
 await page.goto('/observer/?view=topics&mode=records')
 await page.getByRole('button', { name: 'Observer routing Topic', exact: true }).click()
 await page.getByRole('button', { name: '查看 Topic 下的 Issue', exact: true }).click()
 await expect(page).toHaveURL(/view=issues&topicId=/)
 await page.getByRole('button', { name: 'Verify sample build', exact: true }).click()
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByText('Run the sample build and inspect its exit code.', { exact: true })).toBeVisible()
 await expect(dialog.getByText('运行时', { exact: true }).locator('xpath=following-sibling::dd[1]')).toHaveText('agent')
 await expect(dialog.getByText('模型', { exact: true }).locator('xpath=following-sibling::dd[1]')).toHaveText('issue-model')
 await expect(dialog.getByText('推理强度', { exact: true }).locator('xpath=following-sibling::dd[1]')).toHaveText('medium')
 await expect(dialog.getByText('Build notes · artifacts/build-notes.md', { exact: true })).toBeVisible()
 await expect(dialog.getByRole('heading', { name: '业务验收' })).toBeVisible()
 await expect(dialog.getByText('Reviewed the sample build log', { exact: true })).toBeVisible()
 await expect(dialog.getByText('artifacts/build.log', { exact: true })).toBeVisible()
 await expect(dialog).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('issue-acceptance.png'), fullPage: true, animations: 'disabled' })
 await dialog.getByRole('button', { name: '报告历史', exact: true }).click()
 await expect(page).toHaveURL(/view=reports&issueId=/)
 await page.getByRole('button', { name: 'Sample build verified', exact: true }).click()
 await expect(dialog.getByText('Build exited with code 0.', { exact: true })).toBeVisible()
 expect(await dialog.locator('pre.body-text').textContent()).toBe('Build exited with code 0.')
 await expect(dialog.getByText('artifacts/build.log', { exact: true })).toBeVisible()
 await expect(dialog.getByText('历史报告', { exact: false })).toBeVisible()
 await dialog.getByRole('button', { name: '查看 SessionBinding', exact: true }).click()
 await expect(page).toHaveURL(/view=session-bindings&id=/)
 await expect(dialog.getByRole('link', { name: '此会话绑定的工具调用' })).toHaveAttribute('href', /perspective=metrics&id=.*&scope=session-binding/)
 await expect(dialog.getByText('报告提醒', { exact: true }).locator('xpath=following-sibling::dd[1]')).toHaveText('会话绑定已停用')
 await expect(dialog.getByText('Session 已受理', { exact: true })).toBeVisible()
 await expect(dialog.getByText('原生任务状态', { exact: true })).toBeVisible()
 await expect(dialog.getByText('无活跃任务', { exact: true })).toBeVisible()
 await expect(dialog).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('session-binding-evidence.png'), fullPage: true, animations: 'disabled' })
 await expect(dialog.getByRole('button', { name: /^(启动|完成|停止|提交报告|取消|重开)$/ })).toHaveCount(0)
 await dialog.getByRole('button', { name: '此 Issue 的报告历史', exact: true }).click()
 await expect(page).toHaveURL(/view=reports&issueId=/)
 await expect(page.getByRole('button', { name: 'Sample build verified', exact: true })).toBeVisible()
 for (const collection of ['issues', 'session-bindings', 'reports']) {
  expect((await request.get(`/v1/observer/${collection}`)).status()).toBe(200)
  expect((await request.head(`/v1/observer/${collection}`)).status()).toBe(200)
  expect((await request.post(`/v1/observer/${collection}`, { data: {} })).status()).toBe(405)
 }
 await expect(page.getByRole('navigation').getByRole('button', { name: '执行记录', exact: true })).toHaveCount(0)
 expect((await request.get('/v1/observer/executions')).status()).toBe(404)
 expect((await request.get('/v1/observer/bindings')).status()).toBe(404)
 expect(errors).toEqual([])
 expect(methods.length).toBeGreaterThan(0)
 expect(methods.every(method => method === 'GET')).toBeTruthy()
})

test('Blocked Issue shows separate Issue Session observation, dependency and requirements delivery', async ({ page }, testInfo) => {
 await page.goto('/observer/?view=issues&mode=records')
 await page.getByRole('button', { name: 'Review blocked build', exact: true }).click()
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByText('受阻', { exact: true })).toBeVisible()
 await expect(dialog.getByText('Inspect the archived build output.', { exact: true })).toBeVisible()
 await expect(dialog.getByText('目标版本', { exact: true }).locator('xpath=following-sibling::dd[1]')).toHaveText('2')
 const session = dialog.getByText('Issue Session', { exact: true }).locator('xpath=following-sibling::dd[1]')
 await expect(session).toContainText(/未知 · .+ · massd_session_state_unknown/)
 await expect(dialog.getByText('Confirm the archive with the reviewer', { exact: false }).first()).toBeVisible()
 await expect(dialog.getByText('依赖的 Issue', { exact: true })).toBeVisible()
 await expect(dialog.getByText('Session 已受理 · 目标版本 2', { exact: false }).first()).toBeVisible()
 const firstObservation = await session.textContent()
 await expect(session).not.toHaveText(firstObservation ?? '', { timeout: 8000 })
 await expect(session).toContainText(/未知 · .+ · massd_session_state_unknown/)
 await expect(dialog).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('issue-blocked.png'), fullPage: true, animations: 'disabled' })
 await dialog.getByRole('button', { name: '通知事件', exact: true }).click()
 await expect(dialog.getByTestId('routing-record')).toContainText('issue_session_update')
 await page.goto('/observer/?view=issues&mode=records')
 await page.getByRole('button', { name: 'Review blocked build', exact: true }).click()
 await dialog.getByRole('button', { name: /^查看依赖的 Issue mass-t[1-9]\d*-i[1-9]\d*$/ }).click()
 await expect(dialog.getByText('Reviewed the sample build log', { exact: true })).toBeVisible()
})

test('Cancellation and native stop evidence remain separate and read only', async ({ page, request }, testInfo) => {
 const paths: string[] = []
 page.on('request', r => {
  if (r.url().includes('/v1/')) {
   paths.push(new URL(r.url()).pathname)
   expect(r.method()).toBe('GET')
   expect(r.headers()['authorization']).toBeUndefined()
  }
 })
 await page.goto('/observer/?view=issues&mode=records')
 await page.getByRole('button', { name: 'Canceled sample validation', exact: true }).click()
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByText('已取消', { exact: true })).toBeVisible()
 await expect(dialog.getByTestId('lifecycle-history')).toContainText('The sample build was withdrawn')
 await dialog.getByRole('button', { name: '会话绑定历史' }).click()
 await expect(page).toHaveURL(/view=session-bindings&issueId=/)
 await page.getByRole('row').filter({ hasText: '已撤销' }).getByRole('button').click()
 const evidence = dialog.getByTestId('stop-evidence')
 await expect(evidence).toContainText('停止请求')
 await expect(evidence).toContainText('未知')
 await expect(evidence).toContainText('无活跃任务')
 await expect(dialog.getByTestId('release-evidence')).toHaveCount(0)
 await expect(dialog.getByRole('button', { name: /^(取消|重开|恢复|替换|重试|核销)$/ })).toHaveCount(0)
 await expect(dialog).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('cancellation-stop-unknown.png'), fullPage: true, animations: 'disabled' })
 expect(paths.length).toBeGreaterThan(0)
 expect(paths.every(p => p.startsWith('/v1/observer/'))).toBeTruthy()
 expect((await request.post('/v1/observer/session-bindings', { data: {} })).status()).toBe(405)
 expect((await request.post('/v1/operator/input-recovery', { data: {} })).status()).toBe(404)
})


test('A reopened Issue links each report to its own binding and retains shared MASS history', async ({ page, request }) => {
 const audit = `http://127.0.0.1:${process.env.SUPERAGENT_OBSERVER_AUDIT_PORT || '18795'}`
 const seed = await request.get(`${audit}/session-reuse-fixture`)
 expect(seed.ok()).toBeTruthy()
 const fixture = await seed.json()
 const { endpoint, issueId, originalSessionBindingId, currentSessionBindingId, originalReportId, currentReportId, retainedHistory } = fixture
 const [original, current, reports] = await Promise.all([
  request.get(`${endpoint}/v1/observer/session-bindings/${originalSessionBindingId}`),
  request.get(`${endpoint}/v1/observer/session-bindings/${currentSessionBindingId}`),
  request.get(`${endpoint}/v1/observer/reports?issueId=${issueId}`),
 ].map(async response => (await response).json()))
 expect(original).toMatchObject({ id: originalSessionBindingId, state: 'revoked' })
 expect(current).toMatchObject({ id: currentSessionBindingId, state: 'ready', sessionId: original.sessionId, sessionUid: original.sessionUid })
 expect(reports.items).toEqual(expect.arrayContaining([
  expect.objectContaining({ id: originalReportId, current: false, sessionBindingId: originalSessionBindingId }),
  expect.objectContaining({ id: currentReportId, current: true, sessionBindingId: currentSessionBindingId }),
 ]))
 await page.goto(`${endpoint}/observer/?perspective=issue&id=${issueId}`)
 const issue = page.getByRole('article', { name: '当前 Issue' })
 const currentSession = issue.getByRole('link', { name: '查看当前会话 →' })
 await expect(currentSession).toHaveAttribute('href', `/observer/?view=session-bindings&id=${currentSessionBindingId}&mode=records`)
 await issue.getByRole('button', { name: /Report after Session reuse/ }).click()
 await expect(issue.getByText(/^当前报告 · 目标版本/)).toBeVisible()
 await expect(issue.getByRole('link', { name: '查看提交报告的会话' })).toHaveAttribute('href', `/observer/?view=session-bindings&id=${currentSessionBindingId}&mode=records`)
 await issue.getByRole('button', { name: /Report before Session reuse/ }).click()
 await expect(issue.getByText(/^历史报告 · 目标版本/)).toBeVisible()
 await issue.getByRole('link', { name: '查看提交报告的会话' }).click()
 await expect(page).toHaveURL(new RegExp(`view=session-bindings&id=${originalSessionBindingId}(?:&|$)`))
 const dialog = page.getByRole('dialog')
 await expect(dialog.getByTestId('recovery-evidence')).toContainText('已完成 · settle')
 await expect(dialog.getByTestId('release-evidence')).toContainText('native_session_closed')
 const history = dialog.getByRole('region', { name: 'Session 历史' })
 async function expectScopedCalls(sessionBindingId: string) {
  const calls = page.waitForResponse(response => {
   const url = new URL(response.url())
   return url.pathname === '/v1/observer/mcp-tool-usage/calls' && url.searchParams.get('sessionBindingId') === sessionBindingId
  })
  await dialog.getByRole('link', { name: '此会话绑定的工具调用' }).click()
  await expect(page.getByRole('textbox', { name: 'SessionBinding ID' })).toHaveValue(sessionBindingId)
  const result = await calls
  expect(result.ok()).toBeTruthy()
  const { items } = await result.json() as { items: { sessionBindingId: string; tool: string }[] }
  expect(items.filter(call => call.tool === 'submit_report')).toHaveLength(1)
  expect(items.every(call => call.sessionBindingId === sessionBindingId)).toBeTruthy()
  await expect(page.getByRole('region', { name: '工具调用记录' }).getByRole('link', { name: 'submit_report' })).toBeVisible()
  await page.goBack()
 }
 await expect(history.getByText(retainedHistory, { exact: true })).toBeVisible()
 await expectScopedCalls(originalSessionBindingId)
 await page.goBack()
 await currentSession.click()
 await expect(history.getByText(retainedHistory, { exact: true })).toBeVisible()
 await expectScopedCalls(currentSessionBindingId)
 await dialog.getByRole('button', { name: '查看上一个会话绑定' }).click()
 await expect(page).toHaveURL(new RegExp(`view=session-bindings&id=${originalSessionBindingId}(?:&|$)`))
})
