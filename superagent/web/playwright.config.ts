import { defineConfig } from '@playwright/test'
import { audit, auditPort, corePort, main, routingPort } from './tests/endpoints.mjs'

const offline = { GOWORK: 'off', GOPROXY: 'off', GOSUMDB: 'off' }
const fixtureCommand = (name: string) =>
  `go test -mod=readonly -tags superagentweb -run ^${name}$ -count=1 -v -timeout=8m ./pkg/core`

export default defineConfig({
 testDir: './tests', fullyParallel: false, workers: 1, timeout: 30000,
 reporter: process.env.PLAYWRIGHT_JSON_OUTPUT_FILE ? [['line'], ['json']] : undefined,
 use: { baseURL: main, browserName: 'chromium', channel: 'chrome', trace: 'retain-on-failure' },
 webServer: [{
  command: fixtureCommand('TestObserverBrowserFixture'), cwd: '..',
  env: { ...offline, SUPERAGENT_BROWSER_TEST: '1', SUPERAGENT_BROWSER_PORT: corePort },
  wait: { stdout: /observer browser fixture ready at/ }, timeout: 90000,
  gracefulShutdown: { signal: 'SIGTERM', timeout: 15000 },
 }, {
  command: fixtureCommand('TestRoutingObserverBrowserFixture'), cwd: '..',
  env: { ...offline, SUPERAGENT_ROUTING_BROWSER_TEST: '1', SUPERAGENT_ROUTING_BROWSER_PORT: routingPort, SUPERAGENT_OBSERVER_AUDIT_PORT: auditPort },
  url: `${audit}/state`, reuseExistingServer: false, timeout: 90000,
  gracefulShutdown: { signal: 'SIGTERM', timeout: 15000 },
 }],
})
