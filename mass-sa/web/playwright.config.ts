import { defineConfig } from '@playwright/test'
const port = process.env.OBSERVER_BROWSER_PORT || '18788'
const baseURL = `http://127.0.0.1:${port}`
export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1,
  timeout: 30000,
  use: { baseURL, browserName: 'chromium', channel: 'chrome', trace: 'retain-on-failure' },
  webServer: {
    command: 'go test -tags observerweb -run ^TestObserverBrowserFixture$ -count=1 -timeout=10m',
    cwd: '..',
    env: { GOWORK: 'off', OBSERVER_BROWSER_TEST: '1' },
    url: `${baseURL}/observer/api/projects`,
    reuseExistingServer: false,
    timeout: 60000,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 10000 },
  },
})
