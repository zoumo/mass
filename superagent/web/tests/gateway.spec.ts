import { expect, test } from '@playwright/test'
import { httpsOrigin } from './endpoints.mjs'

test.use({ ignoreHTTPSErrors: true })

test('HTTPS gateway serves the built Web and validates Origin before proxying', async ({ page, request }) => {
  const response = await page.goto(httpsOrigin + '/superagent/?view=internal-events')
  expect(response?.status()).toBe(200)
  expect(response?.headers()['content-security-policy']).toContain("script-src 'self'")
  await expect(page).toHaveTitle('Superagent Web')
  await expect(page.getByRole('link', { name: 'Superagent Web 首页' })).toBeVisible()
  await expect(page.getByText('核心服务已就绪', { exact: true })).toBeVisible()
  await page.reload()
  await expect(page.getByText('核心服务已就绪', { exact: true })).toBeVisible()

  const query = httpsOrigin + '/v1/core/threads'
  expect((await request.get(query, { headers: { Origin: httpsOrigin, 'Sec-Fetch-Site': 'same-origin' } })).status()).toBe(200)
  const forged = {
    Forwarded: 'for=127.0.0.1;proto=https;host=foreign.example',
    'X-Forwarded-Proto': 'https',
    'X-Forwarded-Host': 'foreign.example',
    'X-Forwarded-For': '127.0.0.1',
  }
  expect((await request.get(query, { headers: { ...forged, Origin: 'https://foreign.example' } })).status()).toBe(403)
  expect((await request.get(query, { headers: { ...forged, Origin: httpsOrigin, 'Sec-Fetch-Site': 'cross-site' } })).status()).toBe(403)
  for (const action of ['runtime-evidence', '%72untime-evidence', 'recoveries', '%72ecoveries']) {
    const path = `${httpsOrigin}/v1/core/session-bindings/example/${action}`
    expect((await request.fetch(path, { method: action.endsWith('evidence') ? 'GET' : 'POST', headers: forged })).status()).toBe(404)
  }

  const missingAPI = await request.get(httpsOrigin + '/v1/core/topics/missing')
  expect(missingAPI.status()).toBe(404)
  expect(missingAPI.headers()['content-type']).toContain('application/json')
  expect((await request.get(httpsOrigin + '/superagent/assets/missing.js')).status()).toBe(404)
})
