import { defineConfig } from '@playwright/test'
const routingPort = process.env.SUPERAGENT_ROUTING_BROWSER_PORT || '18794'
const port = process.env.SUPERAGENT_BROWSER_PORT || '18793'
const auditPort = process.env.SUPERAGENT_OBSERVER_AUDIT_PORT || '18795'
export default defineConfig({
 testDir: './tests', fullyParallel: false, workers: 1, timeout: 30000,
 reporter: process.env.PLAYWRIGHT_JSON_OUTPUT_FILE ? [['line'], ['json']] : undefined,
 use: { baseURL: `http://127.0.0.1:${port}`, browserName: 'chromium', channel: 'chrome', trace: 'retain-on-failure' },
 webServer: [{
  command: 'go test -mod=readonly -tags observerweb -run ^TestObserverBrowserFixture$ -count=1 -v -timeout=8m ./pkg/core', cwd: '..',
  env: { GOWORK: 'off', GOPROXY: 'off', GOSUMDB: 'off', SUPERAGENT_BROWSER_TEST: '1', SUPERAGENT_BROWSER_PORT: port },
  url: `http://127.0.0.1:${port}/v1/observer/health`, reuseExistingServer: false, timeout: 60000,
  gracefulShutdown: { signal: 'SIGTERM', timeout: 15000 },
 }, {
  command: 'go test -mod=readonly -tags observerweb -run ^TestRoutingObserverBrowserFixture$ -count=1 -v -timeout=8m ./pkg/core', cwd: '..',
  env: { GOWORK: 'off', GOPROXY: 'off', GOSUMDB: 'off', SUPERAGENT_BROWSER_TEST: '1', SUPERAGENT_ROUTING_BROWSER_TEST: '1', SUPERAGENT_ROUTING_BROWSER_PORT: routingPort, SUPERAGENT_OBSERVER_AUDIT_PORT: auditPort },
  url: `http://127.0.0.1:${auditPort}/state`, reuseExistingServer: false, timeout: 60000,
  gracefulShutdown: { signal: 'SIGTERM', timeout: 15000 },
 }],
})
