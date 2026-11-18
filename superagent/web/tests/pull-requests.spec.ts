import { expect, test } from '@playwright/test'

test.use({
 baseURL: `http://127.0.0.1:${process.env.SUPERAGENT_ROUTING_BROWSER_PORT || '18794'}`,
 viewport: { width: 1280, height: 1060 },
})

test('Topic and Issue link to PR Threads, subscriptions and source messages', async ({ page, request }, testInfo) => {
 const internal = 'https://review.example.invalid/code/Repo%2FSDK?change=42&next=a%2fb&utm_source=identity#review%2Fversion'
 const github = 'https://github.com/example/alpha/pull/42'
 const methods: string[] = []
 page.on('request', r => {
  if (r.url().includes('/v1/')) {
   methods.push(r.method())
   expect(r.headers()['authorization']).toBeUndefined()
  }
 })
 await page.goto('/observer/?view=topics')
 await page.getByRole('button', { name: /^Observer routing Topic/ }).click()
 const workContext = page.getByRole('complementary', { name: '工作背景' })
 await expect(workContext.getByRole('link', { name: internal, exact: true })).toBeVisible()
 await expect(workContext.getByRole('link', { name: github, exact: true })).toBeVisible()
 await workContext.getByRole('link', { name: /^Verify sample build/ }).click()
 await expect(workContext.getByRole('link', { name: github, exact: true })).toBeVisible()
 await expect(workContext.getByRole('link', { name: internal, exact: true })).toHaveCount(0)
 await workContext.getByRole('link', { name: '查看订阅', exact: true }).click()
 await expect(page.getByRole('dialog').getByRole('button', { name: `查看线程 ${github}`, exact: true })).toBeVisible()

 await page.goto('/observer/?view=topics&mode=records')
 await page.getByRole('button', { name: 'Observer routing Topic', exact: true }).click()
 await page.getByRole('button', { name: '查看 Topic 拉取请求' }).click()
 await expect(page).toHaveURL(/view=pull-requests&topicId=/)
 await page.getByRole('button', { name: internal, exact: true }).click()
 const detail = page.getByRole('dialog')
 await expect(detail.getByText(internal, { exact: true })).toBeVisible()
 await detail.getByRole('button', { name: '查看订阅', exact: true }).click()
 await detail.getByRole('button', { name: `查看线程 ${internal}`, exact: true }).click()
 await expect(detail.getByText('暂未收到来源消息', { exact: true })).toBeVisible()
 await expect(detail).toHaveCSS('transform', /^(none|matrix\(1, 0, 0, 1, 0, 0\))$/)
 await page.screenshot({ path: testInfo.outputPath('pr-without-source-input.png'), fullPage: true, animations: 'disabled' })
 await detail.getByRole('button', { name: '查看线程订阅' }).click()
 await expect(page).toHaveURL(/view=subscriptions&threadKey=/)
 await expect(page.getByRole('table').getByRole('button', { name: /^sub_/ })).toHaveCount(1)

 await page.getByRole('navigation').getByRole('button', { name: 'Issue', exact: true }).click()
 await page.getByRole('button', { name: 'Verify sample build', exact: true }).click()
 await detail.getByRole('button', { name: '查看 Issue 拉取请求' }).click()
 await expect(page).toHaveURL(/view=pull-requests&issueId=/)
 await expect(page.getByRole('button', { name: internal, exact: true })).toHaveCount(0)
 await page.getByRole('button', { name: github, exact: true }).click()
 await expect(detail.getByRole('button', { name: '查看执行记录' })).toBeVisible()
 await detail.getByRole('button', { name: `查看线程 ${github}`, exact: true }).click()
 await detail.getByRole('button', { name: '查看线程消息' }).click()
 await expect(page).toHaveURL(/view=messages&threadKey=/)
 await page.getByRole('button', { name: 'Synthetic PR review arrived.', exact: true }).click()
 await expect(detail.getByTestId('message-body')).toHaveText('Synthetic PR review arrived.')
 expect(new Set(methods)).toEqual(new Set(['GET']))
 expect((await request.head('/v1/observer/pull-requests')).status()).toBe(200)
 expect((await request.post('/v1/observer/pull-requests', { data: {} })).status()).toBe(405)
})
