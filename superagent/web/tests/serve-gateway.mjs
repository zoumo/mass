import { spawn } from 'node:child_process'
import { setTimeout } from 'node:timers/promises'
import { audit, corePort, fixtureOrigins, httpsOrigin, main, routing, routingPort, webPort } from './endpoints.mjs'

async function ready(url) {
  const deadline = Date.now() + 90_000
  while (Date.now() < deadline) {
    const response = await fetch(url, { signal: AbortSignal.timeout(1000) }).catch(() => undefined)
    if (response?.ok) return response
    await setTimeout(100)
  }
  throw new Error(`Fixture did not become ready at ${url}`)
}

const fixtures = await Promise.all(Object.entries(fixtureOrigins).map(async ([name, origin]) => {
  const response = await ready(`${audit}/${name}`)
  const { endpoint } = await response.json()
  const backend = new URL(endpoint)
  if (backend.protocol !== 'http:' || backend.hostname !== '127.0.0.1' || !backend.port) {
    throw new Error(`Expected loopback Core for ${name}`)
  }
  return [origin, backend.host]
}))
const sites = [[main, `127.0.0.1:${corePort}`], [routing, `127.0.0.1:${routingPort}`], ...fixtures]
const proxy = upstream => `import superagent_gateway 127.0.0.1:${webPort} ${upstream} embedded_massd`
const config = `{
  admin off
  persist_config off
  default_bind 127.0.0.1
  skip_install_trust
  auto_https disable_redirects
}
import gateway.caddy
${sites.map(([origin, upstream]) => `${origin} {
  ${proxy(upstream)}
}`).join('\n')}
${httpsOrigin} {
  tls internal
  ${proxy(`127.0.0.1:${routingPort}`)}
}
`

const caddy = spawn('caddy', ['run', '--adapter', 'caddyfile', '--config', '-'], { stdio: ['pipe', 'inherit', 'inherit'] })
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => caddy.kill(signal))
caddy.on('exit', (code, signal) => { process.exitCode = code ?? (signal === 'SIGTERM' ? 0 : 1) })
caddy.stdin.end(config)
