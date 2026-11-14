// SPDX-License-Identifier: AGPL-3.0-only
import { createRouter, createWebHistory, type Router, type RouteRecordRaw } from 'vue-router'
import { setUnauthorizedHandler } from '@/api/client'
import type { Permission } from '@/auth/permissions'
import { homeLocation } from '@/layouts/navigation'
import { useSessionStore } from '@/stores/session'

declare module 'vue-router' {
  interface RouteMeta {
    /** Only for signed-in users; inherited by child routes. */
    requiresAuth?: boolean
    /** i18n key of the page title shown in the browser tab. */
    titleKey?: string
    /** Key of the sider entry to highlight. */
    navKey?: string
    /** Permission needed to open the page. */
    permission?: Permission
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
        meta: { titleKey: 'dashboard.title', navKey: 'overview', permission: 'system.read' },
      },
      {
        path: 'software',
        name: 'software',
        component: () => import('@/views/SoftwareView.vue'),
        meta: { titleKey: 'software.title', navKey: 'software', permission: 'software.read' },
      },
      {
        path: 'jobs',
        name: 'jobs',
        component: () => import('@/views/JobsView.vue'),
        meta: { titleKey: 'jobs.title', navKey: 'jobs' },
      },
      {
        path: 'jobs/:id',
        name: 'job',
        component: () => import('@/views/jobs/JobDetailView.vue'),
        meta: { titleKey: 'jobs.detail.title', navKey: 'jobs' },
      },
      {
        path: 'users',
        name: 'users',
        component: () => import('@/views/UsersView.vue'),
        meta: { titleKey: 'users.title', navKey: 'users', permission: 'users.manage' },
      },
      {
        path: 'messages',
        name: 'messages',
        component: () => import('@/views/MessagesView.vue'),
        meta: { titleKey: 'messages.title', navKey: 'messages' },
      },
      {
        path: 'audit',
        name: 'audit',
        component: () => import('@/views/AuditView.vue'),
        meta: { titleKey: 'audit.title', navKey: 'audit', permission: 'audit.read' },
      },
      {
        path: 'settings',
        component: () => import('@/views/SettingsView.vue'),
        meta: { titleKey: 'settings.title', navKey: 'settings' },
        children: [
          { path: '', name: 'settings', redirect: { name: 'settings-account' } },
          {
            path: 'account',
            name: 'settings-account',
            component: () => import('@/views/settings/AccountSettings.vue'),
          },
          {
            path: 'security',
            name: 'settings-security',
            component: () => import('@/views/settings/SecuritySettings.vue'),
          },
          {
            path: 'sessions',
            name: 'settings-sessions',
            component: () => import('@/views/settings/SessionsSettings.vue'),
          },
          {
            path: 'tokens',
            name: 'settings-tokens',
            component: () => import('@/views/settings/TokenSettings.vue'),
          },
          {
            path: 'panel',
            name: 'settings-panel',
            component: () => import('@/views/settings/PanelSettings.vue'),
            meta: { permission: 'settings.read' },
          },
          {
            path: 'updates',
            name: 'settings-updates',
            component: () => import('@/views/settings/UpdateSettings.vue'),
            meta: { permission: 'settings.read' },
          },
          {
            path: 'modules',
            name: 'settings-modules',
            component: () => import('@/views/settings/ModuleSettings.vue'),
            meta: { permission: 'settings.read' },
          },
          {
            path: 'notifications',
            name: 'settings-notifications',
            component: () => import('@/views/settings/NotificationSettings.vue'),
            meta: { permission: 'notify.manage' },
          },
          {
            path: 'api',
            name: 'settings-api',
            component: () => import('@/views/settings/ApiReference.vue'),
          },
        ],
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
      return session.isAuthenticated ? homeLocation(router) : { name: 'login' }
    }
    if (to.meta.requiresAuth && !session.isAuthenticated) {
      return loginLocation(to.fullPath)
    }
    if (to.name === 'login' && session.isAuthenticated) {
      return homeLocation(router)
    }
    // Pages the role may not open lead to the first page it may.
    if (to.meta.permission && !session.can(to.meta.permission)) {
      return homeLocation(router)
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
