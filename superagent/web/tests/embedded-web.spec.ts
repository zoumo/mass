import { expect, test } from '@playwright/test'
import { audit, main, routing } from './endpoints.mjs'

test('Core serves the built Web, reloads deep links, and applies the HTTP boundary', async ({ page, request }) => {
  const response = await page.goto('/?view=internal-events')
  expect(response?.status()).toBe(200)
  expect(response?.headers()).toMatchObject({
    'x-content-type-options': 'nosniff',
    'referrer-policy': 'no-referrer',
    'cross-origin-resource-policy': 'same-origin',
    'cache-control': 'no-store',
  })
  expect(response?.headers()['content-security-policy']).toContain("script-src 'self'; style-src 'self'; style-src-attr 'unsafe-inline'; connect-src 'self'")
  await expect(page).toHaveTitle('Superagent Web')
  await expect(page.getByRole('link', { name: 'Superagent Web 首页' })).toBeVisible()
  await expect(page.getByText('核心服务已就绪', { exact: true })).toBeVisible()

  const asset = await page.locator('script[type="module"]').getAttribute('src')
  expect(asset).toMatch(/^\/assets\/.+\.js$/)
  const code = await request.get(asset!)
  expect(code.status()).toBe(200)
  expect(code.headers()['content-type']).toContain('javascript')
  expect((await request.head(asset!)).status()).toBe(200)
  for (const path of ['/', '/topic-conversation', '/message', '/router', '/topic', '/issue', '/trace', '/metrics', '/watchdog']) {
    const head = await request.head(path + '?id=fixture')
    expect(head.status(), path).toBe(200)
    expect(head.headers()['content-type']).toContain('text/html')
    expect(await head.body()).toHaveLength(0)
  }
  await page.getByRole('link', { name: 'Watchdog', exact: true }).click()
  await expect(page).toHaveURL(main + '/watchdog')
  expect((await page.reload())?.status()).toBe(200)
  await expect(page.getByRole('link', { name: 'Superagent Web 首页' })).toBeVisible()

  for (const path of ['/', '/issue', asset!]) {
    const result = await request.post(path)
    expect(result.status(), path).toBe(405)
    expect(result.headers()['allow']).toBe('GET, HEAD')
  }
  for (const path of ['/assets/', '/assets/missing', '/assets/missing.js', '/unknown', '/superagent', '/superagent/',
    '/superagent/topic', '/v1/core/topics', '/massd/jsonrpc', '/apis', '/apis/unknown', '/apis/core/v1/unknown',
    '/apis/massd/unknown', '/status/unknown', '/livez/unknown', '/readyz/unknown']) {
    const result = await request.get(path)
    expect(result.status(), path).toBe(404)
    expect(result.headers()['content-type']).not.toContain('text/html')
  }

  const query = '/apis/core/v1/threads'
  expect((await request.get(query, { headers: { Origin: main, 'Sec-Fetch-Site': 'same-origin' } })).status()).toBe(200)
  const forged = { Forwarded: 'proto=https;host=foreign.example', 'X-Forwarded-Host': 'foreign.example' }
  for (const path of ['/', asset!, query, '/apis/massd/readyz']) {
    expect((await request.get(path, { headers: { ...forged, Origin: 'http://foreign.example' } })).status()).toBe(403)
    expect((await request.get(path, { headers: { ...forged, Origin: main, 'Sec-Fetch-Site': 'cross-site' } })).status()).toBe(403)
  }
  const operator = '/apis/core/v1/session-bindings/example/runtime-evidence'
  expect((await request.get(operator)).status()).toBe(401)
  expect((await request.get(operator.replace('/runtime-', '/%72untime-'))).status()).toBe(404)
  expect((await request.get(operator, { headers: { 'Sec-Fetch-Site': 'same-origin' } })).status()).toBe(403)
  for (const path of ['/livez', '/readyz', '/status', '/apis/massd/livez', '/apis/massd/readyz']) {
    const probe = await request.get(path)
    expect(probe.status(), path).toBe(200)
    expect(probe.headers()['content-type']).toContain('application/json')
  }
})

test('browser telemetry uses the Core origin and reaches the OTLP receiver when enabled', async ({ page, request }) => {
  for (const signal of ['traces', 'metrics']) {
    const response = await request.post(`/apis/core/v1/telemetry/${signal}`, {
      headers: { 'Content-Type': 'application/x-protobuf' }, data: Buffer.alloc(0),
    })
    expect(response.status()).toBe(404)
  }
  if (process.env.VITE_SUPERAGENT_WEB_TELEMETRY !== 'on') return

  const traces = page.waitForResponse(`${routing}/apis/core/v1/telemetry/traces`)
  const metrics = page.waitForResponse(`${routing}/apis/core/v1/telemetry/metrics`)
  await page.goto(routing + '/?view=internal-events')
  await expect(page.getByText('核心服务已就绪', { exact: true })).toBeVisible()
  expect((await traces).status()).toBe(200)
  expect((await metrics).status()).toBe(200)
  const received = await (await request.get(audit + '/web-telemetry')).json()
  expect(received.traces).toBeGreaterThan(0)
  expect(received.metrics).toBeGreaterThan(0)
})
