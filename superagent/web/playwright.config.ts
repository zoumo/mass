import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { defineConfig } from '@playwright/test'
import { audit, auditPort, corePort, main, routingPort, webPort } from './tests/endpoints.mjs'
const caddyData = mkdtempSync(join(tmpdir(), 'superagent-web-caddy-'))
const caddyEnv = { XDG_DATA_HOME: caddyData, XDG_CONFIG_HOME: caddyData }
export default defineConfig({
 testDir: './tests', fullyParallel: false, workers: 1, timeout: 30000,
 reporter: process.env.PLAYWRIGHT_JSON_OUTPUT_FILE ? [['line'], ['json']] : undefined,
 use: { baseURL: main, browserName: 'chromium', channel: 'chrome', trace: 'retain-on-failure' },
 webServer: [{
  command: 'go test -mod=readonly -run ^TestObserverBrowserFixture$ -count=1 -v -timeout=8m ./pkg/core', cwd: '..',
  env: { GOWORK: 'off', GOPROXY: 'off', GOSUMDB: 'off', SUPERAGENT_BROWSER_TEST: '1', SUPERAGENT_BROWSER_PORT: corePort },
  url: `http://127.0.0.1:${corePort}/readyz`, reuseExistingServer: false, timeout: 60000,
  gracefulShutdown: { signal: 'SIGTERM', timeout: 15000 },
 }, {
  command: 'go test -mod=readonly -run ^TestRoutingObserverBrowserFixture$ -count=1 -v -timeout=8m ./pkg/core', cwd: '..',
  env: { GOWORK: 'off', GOPROXY: 'off', GOSUMDB: 'off', SUPERAGENT_BROWSER_TEST: '1', SUPERAGENT_ROUTING_BROWSER_TEST: '1', SUPERAGENT_ROUTING_BROWSER_PORT: routingPort, SUPERAGENT_OBSERVER_AUDIT_PORT: auditPort },
  url: `${audit}/state`, reuseExistingServer: false, timeout: 60000,
  gracefulShutdown: { signal: 'SIGTERM', timeout: 15000 },
 }, {
  command: 'caddy run --config Caddyfile',
  env: { ...caddyEnv, SUPERAGENT_WEB_LISTEN: `127.0.0.1:${webPort}` },
  url: `http://127.0.0.1:${webPort}/superagent/`, reuseExistingServer: false,
  gracefulShutdown: { signal: 'SIGTERM', timeout: 5000 },
 }, {
  command: 'node tests/serve-gateway.mjs',
  env: caddyEnv,
  url: `${main}/superagent/`, reuseExistingServer: false, timeout: 95000,
  gracefulShutdown: { signal: 'SIGTERM', timeout: 5000 },
 }],
})
