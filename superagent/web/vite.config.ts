import { defineConfig, type Plugin, type ProxyOptions } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import { readFileSync } from 'node:fs'

const backendPaths = '^/(?:v1/(?:core/|telemetry/|mcp(?:\\?|$))|(?:status|livez|readyz)(?:\\?|$))'
const backendPath = new RegExp(backendPaths)
const operatorPath = /^\/v1\/core\/session-bindings\/.*\/(?:runtime-evidence|recoveries)$/

function sameOriginProxy(): Plugin {
  return {
    name: 'superagent-proxy-boundary',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const rawPath = (req.url || '/').split('?', 1)[0]
        let path: string
        try {
          path = decodeURIComponent(rawPath)
        } catch {
          res.writeHead(400).end()
          return
        }
        if (!backendPath.test(path) && !path.startsWith('/massd/')) return next()
        const origin = req.headers.origin
        const site = req.headers['sec-fetch-site']
        if (origin && origin !== `http://${req.headers.host}` || site && site !== 'same-origin' && site !== 'none') {
          res.writeHead(403).end()
          return
        }
        if (operatorPath.test(path)) {
          res.writeHead(404).end()
          return
        }
        next()
      })
    },
  }
}

function proxy(target: string, massd = false): ProxyOptions {
  return {
    target,
    changeOrigin: true,
    ws: massd,
    configure(server) {
      const prepare = (out: import('node:http').ClientRequest) => {
        for (const name of out.getHeaderNames()) {
          if (name === 'forwarded' || name.startsWith('x-forwarded-')) out.removeHeader(name)
        }
        if (!massd && out.hasHeader('origin')) out.setHeader('origin', new URL(target).origin)
      }
      server.on('proxyReq', prepare)
      server.on('proxyReqWs', prepare)
    },
  }
}

// Explicitly enabled for a trusted single-user loopback development session.
function prototypeChatProxy(): Record<string, ProxyOptions> {
  const tokenFile = process.env.SUPERAGENT_CHAT_MASSD_TOKEN_FILE
  if (!tokenFile) return {}
  const target = new URL(process.env.SUPERAGENT_CHAT_MASSD_ENDPOINT ||
    new URL('/massd/jsonrpc', process.env.SUPERAGENT_CORE_ORIGIN || 'http://127.0.0.1:24370').href)
  if (target.protocol !== 'http:' || target.hostname !== '127.0.0.1' ||
      !['/massd/jsonrpc', '/jsonrpc'].includes(target.pathname) || target.username || target.password || target.search || target.hash) {
    throw new Error('The prototype requires a loopback HTTP massd endpoint')
  }
  const token = readFileSync(tokenFile, 'utf8').trim()
  if (!token || /\s/.test(token)) throw new Error('Invalid prototype massd token file')
  return {
    '^/__prototype/massd-chat$': {
      target: target.origin,
      ws: true,
      changeOrigin: true,
      rewrite: () => target.pathname,
      bypass(req, res) {
        const host = req.headers.host || ''
        if (res || req.socket.remoteAddress !== '127.0.0.1' || !/^127\.0\.0\.1:\d+$/.test(host) ||
            req.headers.origin !== `http://${host}`) return false
      },
      configure(server) {
        server.on('proxyReqWs', out => {
          for (const name of out.getHeaderNames()) {
            if (['origin', 'cookie', 'forwarded'].includes(name) || name.startsWith('x-forwarded-')) out.removeHeader(name)
          }
          out.setHeader('Authorization', `Bearer ${token}`)
        })
      },
    },
  }
}

export default defineConfig(({ command }) => ({
  plugins: [sameOriginProxy(), tailwindcss()],
  base: '/superagent/',
  build: { outDir: 'dist' },
  server: {
    proxy: {
      [backendPaths]: proxy(process.env.SUPERAGENT_CORE_ORIGIN || 'http://127.0.0.1:24370'),
      '/massd/': proxy(process.env.SUPERAGENT_CORE_ORIGIN || 'http://127.0.0.1:24370', true),
      ...(command === 'serve' ? prototypeChatProxy() : {}),
    },
  },
}))
