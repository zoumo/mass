import { expect, test } from '@playwright/test'

test('Aura component styles use a fresh page nonce without allowing inline scripts or attributes', async ({ page }) => {
  const violations: string[] = []
  page.on('console', message => {
    if (/content security policy|violates.*directive/i.test(message.text())) violations.push(message.text())
  })
  const response = await page.goto('/observer/triggers?project=alpha&refresh=paused')
  const nonce = await page.locator('meta[name="observer-style-nonce"]').getAttribute('content')
  expect(nonce).toMatch(/^[A-Za-z0-9+/]{32}$/)
  const policy = response!.headers()['content-security-policy']
  expect(policy).toContain(`style-src 'self' 'nonce-${nonce}'`)
  expect(policy).toContain("script-src 'self';")
  expect(policy).not.toContain('unsafe-inline')
  const first = page.getByRole('link', { name: 'Router 后交 Worker', exact: true })
  await first.click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await page.getByRole('combobox', { name: '输入路径', exact: true }).click()
  await expect(page.getByRole('listbox')).toBeVisible()
  await page.keyboard.press('Escape')
  const next = await page.request.get('/observer/triggers')
  const nextHTML = await next.text()
  const nextNonce = nextHTML.match(/name="observer-style-nonce" content="([^"]+)"/)?.[1]
  expect(nextNonce).toBeTruthy()
  expect(nextNonce).not.toBe(nonce)
  expect(violations).toEqual([])
})
