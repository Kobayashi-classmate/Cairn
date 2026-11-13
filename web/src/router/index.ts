// SPDX-License-Identifier: AGPL-3.0-only
import { createRouter, createWebHistory, type Router, type RouteRecordRaw } from 'vue-router'
import { setUnauthorizedHandler } from '@/api/client'
import { useSessionStore } from '@/stores/session'

declare module 'vue-router' {
  interface RouteMeta {
    /** Only for signed-in users; inherited by child routes. */
    requiresAuth?: boolean
    /** i18n key of the page title shown in the browser tab. */
    titleKey?: string
    /** Key of the sider entry to highlight. */
    navKey?: string
  }
}

const routes: RouteRecordRaw[] = [
  {
    path: '/setup',
    name: 'setup',
    component: () => import('@/views/SetupView.vue'),
    meta: { titleKey: 'setup.title' },
  },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { titleKey: 'auth.login.title' },
  },
  {
    path: '/',
    component: () => import('@/layouts/AppLayout.vue'),
    meta: { requiresAuth: true },
    children: [
      {
        path: '',
        name: 'overview',
        component: () => import('@/views/DashboardView.vue'),
        meta: { titleKey: 'dashboard.title', navKey: 'overview' },
      },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

function loginLocation(returnTo: string) {
  return { name: 'login', query: returnTo === '/' ? {} : { redirect: returnTo } }
}

export function createAppRouter(): Router {
  const router = createRouter({
    history: createWebHistory(import.meta.env.BASE_URL),
    routes,
  })

  router.beforeEach(async (to) => {
    const session = useSessionStore()
    await session.init()
    // App.vue renders the startup error instead of any page.
    if (session.bootError) return true

    if (session.setupRequired) {
      if (to.name === 'setup') return true
      // Keep the one-time token if the setup link pointed somewhere else.
      const token = to.query.token
      return { name: 'setup', query: typeof token === 'string' ? { token } : {} }
    }
    if (to.name === 'setup') {
      return session.isAuthenticated ? { name: 'overview' } : { name: 'login' }
    }
    if (to.meta.requiresAuth && !session.isAuthenticated) {
      return loginLocation(to.fullPath)
    }
    if (to.name === 'login' && session.isAuthenticated) {
      return { name: 'overview' }
    }
    return true
  })

  // A 401 on any authenticated call means the session is gone: return to the
  // sign-in page and come back to the current page afterwards.
  setUnauthorizedHandler((error) => {
    const session = useSessionStore()
    if (!session.sessionLost(error)) return
    const current = router.currentRoute.value
    if (current.meta.requiresAuth) {
      void router.replace(loginLocation(current.fullPath))
    }
  })

  return router
}
