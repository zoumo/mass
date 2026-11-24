import { expect, test } from '@playwright/test'

test.use({
 baseURL: `http://127.0.0.1:${process.env.SUPERAGENT_ROUTING_GATEWAY_PORT || '18894'}`,
 viewport: { width: 1280, height: 1060 },
})

test('Topic and Issue preserve their external PR links and source messages', async ({ page, request }, testInfo) => {
 const internal = 'https://review.example.invalid/code/Repo%2FSDK?change=42&next=a%2fb&utm_source=identity#review%2Fversion'
 const github = 'https://github.com/example/alpha/pull/42'
 const methods: string[] = []
 page.on('request', r => {
  if (r.url().includes('/v1/')) {
   methods.push(r.method())
   expect(r.headers()['authorization']).toBeUndefined()
  }
 })
 await page.goto('/superagent/topic')
 await page.getByRole('link', { name: /Observer routing Topic/ }).click()
 const workContext = page.getByRole('article')
 await workContext.getByText(/关注的拉取请求/).click()
 await expect(workContext.getByRole('link', { name: internal, exact: true })).toBeVisible()
 await expect(workContext.getByRole('link', { name: github, exact: true })).toBeVisible()
 await workContext.getByRole('link', { name: '所属 Issue' }).click()
 await page.getByRole('complementary').getByRole('link', {name:/Verify sample build/}).click()
 await expect(workContext.getByRole('link', { name: github, exact: true })).toBeVisible()
 await expect(workContext.getByRole('link', { name: internal, exact: true })).toHaveCount(0)

 await expect(workContext.getByRole('link', { name: github, exact: true })).toHaveAttribute('href', github)

 // The Thread URL includes nested escapes, slashes, query parameters and a fragment.
 const thread = await request.get('/v1/core/threads/' + encodeURIComponent(internal))
 expect(thread.status()).toBe(200)
 expect(await thread.json()).toMatchObject({ id: internal })
 await page.goto('/superagent/?view=threads&id=' + encodeURIComponent(internal))
 const detail = page.getByRole('dialog')
 await expect(detail.getByText('暂未收到来源消息', { exact: true })).toBeVisible()
 await page.screenshot({ path: testInfo.outputPath('pr-without-source-input.png'), fullPage: true, animations: 'disabled' })

 await page.goto('/superagent/?view=threads&id=' + encodeURIComponent(github))
 await detail.getByRole('button', { name: '查看线程消息' }).click()
 await expect(page).toHaveURL(/view=messages&threadId=/)
 await page.getByRole('button', { name: 'Synthetic PR review arrived.', exact: true }).click()
 await expect(detail.getByTestId('message-body')).toHaveText('Synthetic PR review arrived.')
 expect(new Set(methods)).toEqual(new Set(['GET']))
})
