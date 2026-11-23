const loopback = port => `http://127.0.0.1:${port}`

export const corePort = process.env.SUPERAGENT_BROWSER_PORT || '18793'
export const routingPort = process.env.SUPERAGENT_ROUTING_BROWSER_PORT || '18794'
export const auditPort = process.env.SUPERAGENT_OBSERVER_AUDIT_PORT || '18795'
export const audit = loopback(auditPort)
export const main = loopback(process.env.SUPERAGENT_BROWSER_GATEWAY_PORT || '18893')
export const routing = loopback(process.env.SUPERAGENT_ROUTING_GATEWAY_PORT || '18894')
export const webPort = process.env.SUPERAGENT_BROWSER_WEB_PORT || '18898'
export const httpsOrigin = `https://127.0.0.1:${process.env.SUPERAGENT_BROWSER_HTTPS_PORT || '18899'}`
export const fixtureOrigins = {
  'message-path-fixture': loopback(process.env.SUPERAGENT_MESSAGE_PATH_GATEWAY_PORT || '18895'),
  'topic-conversation-fixture': loopback(process.env.SUPERAGENT_CONVERSATION_GATEWAY_PORT || '18896'),
  'session-reuse-fixture': loopback(process.env.SUPERAGENT_SESSION_REUSE_GATEWAY_PORT || '18897'),
}
