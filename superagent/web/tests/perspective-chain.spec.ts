import { expect, test, type Page } from '@playwright/test'

const audit = 'http://127.0.0.1:' + (process.env.SUPERAGENT_OBSERVER_AUDIT_PORT || '18795')
type Issue = { id: string; title: string; topicId: string; materials: { messageId?: string }[] }

for (const [size, width, height] of [
  ['desktop', 1380, 920],
  ['narrow', 390, 844],
] as const) {
  test(`follow one persisted message through Router, Topic, Issue and back to its material (${size})`, async ({
    page,
    context,
    request,
  }, info) => {
    const { endpoint } = await (await request.get(audit + '/message-path-fixture')).json()
    const get = async (path: string) => (await request.get(endpoint + '/v1/observer/' + path)).json()
    const messages = (await get('messages?limit=200')).items
    const msg = messages.find((m: { sourceMessageId: string }) => m.sourceMessageId === 'observer-path-rerouted')
    const topic = (await get('topics')).items.find((t: { title: string }) => t.title === '发布准备')
    const issues: Issue[] = (await get('issues?messageId=' + msg.id)).items
    const issue = issues.find((i) => i.topicId === topic.id)!
    expect(issue.materials.some((m) => m.messageId === msg.id)).toBe(true)
    const methods = new Set<string>()
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(e.message))
    page.on('request', (req) => {
      if (new URL(req.url()).pathname.startsWith('/v1/')) {
        expect(new URL(req.url()).pathname).toMatch(/^\/v1\/observer\//)
        methods.add(req.method())
      }
    })
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await page.addInitScript(() => {
      document.addEventListener(
        'securitypolicyviolation',
        (ev) => (document.documentElement.dataset.cspViolation = ev.effectiveDirective),
      )
    })
    await page.setViewportSize({ width, height })
    const documentResponse = await page.goto(endpoint + '/observer/?perspective=message')
    expect(documentResponse?.headers()['content-security-policy']).toContain("script-src 'self'")
    const capture = async (name: string) => {
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width)
      await page.screenshot({ path: info.outputPath(name + '-' + size + '.png'), animations: 'disabled' })
    }
    const verifyCopy = async (area: ReturnType<Page['locator']>) => {
      await area.getByRole('button', { name: '复制链接' }).click()
      const copied = new URL(await page.evaluate(() => navigator.clipboard.readText()))
      expect(copied.pathname + copied.search).toBe(new URL(page.url()).pathname + new URL(page.url()).search)
    }
    await expect(page.getByRole('heading', { name: 'Message', exact: true })).toBeVisible()
    await page
      .getByRole('complementary', { name: '最近消息' })
      .getByRole('link', { name: /发布前检查/ })
      .click()
    const graph = page.getByRole('region', { name: '消息路径' })
    await expect(graph.getByRole('button', { name: 'Topic 发布准备' }).first()).toBeVisible()
    await page.evaluate(() => scrollTo(0, 0))
    await capture('message')
    const edge = graph.locator('[data-event-id]').first()
    await edge.focus()
    await page.keyboard.press('Enter')
    const facts = graph.getByRole('region', { name: '路径详情' })
    await expect(facts).toBeFocused()
    await expect(facts).toContainText('登记时间')
    await facts.scrollIntoViewIfNeeded()
    await capture('delivery')
    await page.keyboard.press('Escape')
    await expect(edge).toBeFocused()
    await graph.getByRole('button', { name: 'Router', exact: true }).first().click()
    await facts.getByRole('link', { name: '查看 Router' }).click()
    const router = page.locator('article[data-message-id="' + msg.id + '"]')
    await expect(router.getByRole('button', { expanded: true })).toBeVisible()
    await expect(router).toContainText('发布准备与值班交接分别核对各自的检查项')
    await page.getByRole('combobox').selectOption('50')
    const routerURL = page.url()
    await verifyCopy(router)
    await page.reload()
    await expect(page.getByRole('combobox')).toHaveValue('50')
    await expect(router.getByRole('heading', { name: '路由事件记录' })).toBeVisible()
    await router.locator('.router-toggle').scrollIntoViewIfNeeded()
    await capture('router')
    await router.locator('.router-reasons').getByRole('link', { name: topic.title, exact: true }).click()
    const topicView = page.getByRole('article', { name: '当前 Topic' })
    await expect(topicView.getByRole('heading', { name: topic.title, exact: true })).toBeVisible()
    await topicView
      .getByRole('navigation', { name: '筛选活动' })
      .getByRole('link', { name: 'Issue', exact: true })
      .click()
    await verifyCopy(topicView)
    const topicURL = page.url()
    await page.goBack()
    await page.goBack()
    await expect(page).toHaveURL(routerURL)
    await page.goForward()
    await page.goForward()
    await expect(page).toHaveURL(topicURL)
    await expect(topicView.locator('.topic-feed > li').first()).toBeVisible()
    await page.evaluate(() => scrollTo(0, 0))
    await capture('topic')
    await topicView.getByRole('link', { name: '所属 Issue' }).click()
    await page
      .getByRole('complementary')
      .getByRole('link', { name: new RegExp(issue.title) })
      .click()
    const issueView = page.getByRole('article', { name: '当前 Issue' })
    await expect(issueView.getByRole('heading', { name: issue.title })).toBeVisible()
    await page.evaluate(() => scrollTo(0, 0))
    await capture('issue')
    await verifyCopy(issueView)
    await issueView.getByText('来源材料', { exact: false }).first().click()
    const material = issueView.getByRole('link', { name: '发布前检查', exact: true })
    await expect(material).toHaveAttribute('href', new RegExp(msg.id))
    await material.click()
    await page.getByText('查看全文', { exact: true }).click()
    await expect(page.getByTestId('message-body')).toHaveText(msg.text)
    expect(new URL(page.url()).searchParams.get('id')).toBe(msg.id)
    expect([...methods]).toEqual(['GET'])
    expect(errors).toEqual([])
    expect(await page.locator('html').getAttribute('data-csp-violation')).toBeNull()
  })
}

for (const width of [1380, 390]) {
  test(`each primary tab opens its own populated flow at ${width}px`, async ({ page, request }, info) => {
    const { endpoint } = await (await request.get(audit + '/message-path-fixture')).json()
    await page.setViewportSize({ width, height: width === 390 ? 844 : 920 })
    await page.goto(endpoint + '/observer/')
    const tabs = page.getByRole('navigation', { name: '观察视角' })
    for (const [tab, region] of [
      ['Message', '消息路径'],
      ['Router', 'Router 处理流程'],
      ['Topic', 'Topic 活动流程'],
      ['Issue', 'Issue 进展流程'],
    ]) {
      await tabs.getByRole('link', { name: tab, exact: true }).click()
      await expect(tabs.getByRole('link', { name: tab, exact: true })).toHaveAttribute('aria-current', 'page')
      if (tab === 'Message' && width === 390) {
        await page.getByRole('complementary', { name: '最近消息' }).getByRole('link').first().click()
      }
      const graph = page.getByRole('region', { name: region, exact: true })
      await expect(graph).toBeVisible()
      const businessArea =
        tab === 'Router'
          ? page.getByRole('region', { name: '最近路由消息' })
          : page.getByRole('article', { name: '当前' + (tab === 'Message' ? '消息' : ' ' + tab), exact: true })
      await expect(businessArea).toBeVisible()
      if (tab === 'Router') await expect(businessArea.locator('[data-message-id]').first()).toBeVisible()
      else await expect(businessArea.getByRole('heading', { level: 2 }).first()).toBeVisible()
      if (tab==='Message') {
        await expect(graph.locator('.path-node').first()).toBeVisible()
      } else {
        const lane = graph.locator('[data-flow-id]').first()
        await expect(lane.locator('.flow-node').nth(1)).toBeVisible()
        await expect(lane.locator('.flow-edge').first()).toContainText(/\S/)
      }
      await expect(graph).not.toContainText(/正在读取|读取失败/)
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width)
      await graph.scrollIntoViewIfNeeded()
      await page.screenshot({
        path: info.outputPath('tab-' + tab.toLowerCase() + '-' + width + '.png'),
        animations: 'disabled',
      })
    }
  })
}
