const loopback = port => `http://127.0.0.1:${port}`

export const corePort = process.env.SUPERAGENT_BROWSER_PORT || '18793'
export const routingPort = process.env.SUPERAGENT_ROUTING_BROWSER_PORT || '18794'
export const auditPort = process.env.SUPERAGENT_OBSERVER_AUDIT_PORT || '18795'
export const audit = loopback(auditPort)
export const main = loopback(corePort)
export const routing = loopback(routingPort)
export const fixtureTriggerHeaders = {
  Authorization: 'Bearer ' + 'a'.repeat(48),
  'X-Superagent-Source': 'dws',
  'X-Superagent-Account-Id': 'account-1',
}

export async function fixtureOrigin(request, name) {
  const response = await request.get(`${audit}/${name}`)
  if (!response.ok()) throw new Error(`Fixture ${name} returned HTTP ${response.status()}`)
  return (await response.json()).endpoint
}
