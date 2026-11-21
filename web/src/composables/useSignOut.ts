// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { errorMessage } from '@/i18n/errors'
import { useJobDockStore } from '@/stores/jobDock'
import { useSessionStore } from '@/stores/session'

/** Ends the session and returns to the sign-in page. */
export function useSignOut() {
  const router = useRouter()
  const session = useSessionStore()
  const dock = useJobDockStore()
  const signingOut = ref(false)

  async function signOut(): Promise<void> {
    if (signingOut.value) return
    signingOut.value = true
    try {
      await session.signOut()
      // The next account to sign in on this tab does not inherit the cards.
      dock.clear()
      await router.replace({ name: 'login' })
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      signingOut.value = false
    }
  }

  return { signingOut, signOut }
}
