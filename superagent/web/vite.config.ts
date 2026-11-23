import { defineConfig, type Plugin, type ProxyOptions } from 'vite'
import tailwindcss from '@tailwindcss/vite'

const backendPaths = '^/(?:v1/(?:core/|telemetry/|mcp(?:\\?|$))|(?:status|livez|readyz)(?:\\?|$))'
const backendPath = new RegExp(backendPaths)
const operatorPath = /^\/v1\/core\/(?:session-bindings\/.*\/(?:runtime-evidence|recoveries)$|scheduled-tasks(?:\/|$))/

function sameOriginProxy(): Plugin {
  return {
    name: 'superagent-proxy-boundary',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const rawTarget = req.url || '/'
        const queryStart = rawTarget.indexOf('?')
        const rawPath = queryStart === -1 ? rawTarget : rawTarget.slice(0, queryStart)
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
        const q = new URLSearchParams(queryStart === -1 ? '' : rawTarget.slice(queryStart + 1))
        const operatorFilter = path === '/v1/core/trigger-messages' && (q.has('source') || q.has('scheduledTaskId'))
        if (operatorPath.test(path) || operatorFilter) {
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

export default defineConfig({
  plugins: [sameOriginProxy(), tailwindcss()],
  base: '/superagent/',
  build: { outDir: 'dist' },
  server: {
    proxy: {
      [backendPaths]: proxy(process.env.SUPERAGENT_CORE_ORIGIN || 'http://127.0.0.1:24370'),
      '/massd/': proxy(process.env.SUPERAGENT_CORE_ORIGIN || 'http://127.0.0.1:24370', true),
    },
  },
})
