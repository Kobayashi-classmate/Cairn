// SPDX-License-Identifier: AGPL-3.0-only
import { computed, reactive, ref, watch, type Ref } from 'vue'
import type { CertView } from '@/api/certs'
import { isStepUpRequired } from '@/api/client'
import type { SiteView } from '@/api/sites'
import {
  formFromSite,
  siteRequest,
  validateSiteForm,
  type FieldErrors,
  type SiteField,
  type SiteForm,
} from '@/sites/form'
import { saveSite } from '../actions'

/**
 * State of one sub-item of the site drawer: a copy of the whole site as a
 * form, of which the sub-item edits a few fields. Saving checks those
 * fields and sends the whole site again (PUT /sites/{code}); the job goes
 * to the job dock. The copy follows the site whenever the site changes on
 * the server (its `updatedAt`).
 */
export function useSitePane(
  site: Ref<SiteView>,
  fields: readonly SiteField[],
  certs: Ref<readonly CertView[] | null> = ref(null),
) {
  const form = reactive<SiteForm>(formFromSite(site.value))
  const errors = ref<FieldErrors>({})
  const saving = ref(false)
  const error = ref<unknown>(null)

  function reset() {
    Object.assign(form, formFromSite(site.value))
    errors.value = {}
    error.value = null
  }

  watch(() => [site.value.code, site.value.updatedAt], reset)

  /** The form differs from the site. */
  const dirty = computed(
    () => JSON.stringify(siteRequest(form, 'edit')) !== JSON.stringify(siteRequest(formFromSite(site.value), 'edit')),
  )

  /** A change is being applied: wait for it before starting another. */
  const busy = computed(() => saving.value || site.value.state === 'applying')

  /** Checks the sub-item's fields and saves the site; resolves true once the job started. */
  async function save(): Promise<boolean> {
    errors.value = validateSiteForm(form, 'edit', certs.value, fields)
    if (Object.keys(errors.value).length > 0) return false
    saving.value = true
    error.value = null
    try {
      await saveSite(site.value, siteRequest(form, 'edit'))
      return true
    } catch (err) {
      if (!isStepUpRequired(err)) error.value = err
      return false
    } finally {
      saving.value = false
    }
  }

  return { form, errors, saving, error, dirty, busy, save, reset }
}
