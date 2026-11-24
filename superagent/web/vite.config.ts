import { defineConfig, type Plugin, type ProxyOptions } from 'vite'
import tailwindcss from '@tailwindcss/vite'

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
