import { defineConfig } from '@playwright/test'
export default defineConfig({
  testDir: './resource-tests', testMatch: 'resources.spec.ts', workers: 1, timeout: 45000,
  use: { baseURL: 'http://127.0.0.1:18795', browserName: 'chromium', channel: 'chrome', trace: 'retain-on-failure' },
  webServer: {
    command: 'go test -tags observerweb -run ^TestObserverResourceBrowserFixture$ -count=1 -timeout=10m', cwd: '..',
    env: { GOWORK: 'off', OBSERVER_RESOURCE_BROWSER_TEST: '1' },
    url: 'http://127.0.0.1:18795/observer/api/projects', reuseExistingServer: false, timeout: 60000,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 10000 },
  },
})
