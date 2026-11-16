import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory('/observer/'),
  routes: [
    { path: '/', redirect: '/triggers' },
    { path: '/routers', name: 'routers', component: () => import('./pages/RoutersPage.vue') },
    { path: '/triggers', name: 'triggers', component: () => import('./pages/TriggersPage.vue') },
    { path: '/resources', name: 'resources', component: () => import('./pages/ResourcesPage.vue') },
    { path: '/tickets', name: 'tickets', component: () => import('./pages/TicketsPage.vue') },
    { path: '/:pathMatch(.*)*', component: () => import('./pages/PendingPage.vue') },
  ],
})

router.beforeEach(to => {
  const obsolete = Object.keys(to.query).filter(key => /multica/i.test(key) || (to.name === 'tickets' && key === 'status'))
  if (!obsolete.length) return
  const query = { ...to.query }
  for (const key of obsolete) delete query[key]
  // An old cursor belongs to the removed filter's snapshot.
  delete query.cursor
  return { path: to.path, query, hash: to.hash, replace: true }
})
