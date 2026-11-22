// SPDX-License-Identifier: AGPL-3.0-only
//
// Database server actions shared by the service cards, the lists and their
// "more" menus. Changes run as jobs that the job dock follows; deleting
// asks for the name and for the password (step-up).
import {
  controlService,
  createDump,
  deleteDump,
  dropDatabase,
  dropDbUser,
  dumpUrl,
  setRemoteAccess,
  type DbDatabase,
  type DbEngine,
  type DbUser,
  type DumpInfo,
} from '@/api/databases'
import { isStepUpRequired } from '@/api/client'
import type { Job } from '@/api/jobs'
import { ensureElevated } from '@/auth/stepUp'
import { confirmAction } from '@/composables/useConfirmDialog'
import { engineLabel } from '@/databases/labels'
import { i18n } from '@/i18n'
import { notifyError, notifyInfo, notifySuccess } from '@/lib/notify'
import type { Tone } from '@/lib/tone'
import { useDatabasesStore } from '@/stores/databases'

export function engineTone(engine: DbEngine): Tone {
  if (engine.error) return 'destructive'
  return engine.running ? 'success' : 'neutral'
}

export function engineStateText(engine: DbEngine): string {
  const { t } = i18n.global
  if (engine.error) return t('databases.state.error')
  return engine.running ? t('databases.running') : t('databases.stopped')
}

/** Starts or restarts a server at once; stopping asks first, naming who loses the connection. */
export async function serviceAction(engine: DbEngine, action: 'start' | 'stop' | 'restart'): Promise<void> {
  const store = useDatabasesStore()
  const start = () => controlService(engine.id, action)
  if (action !== 'stop') {
    await store.runJob(start)
    return
  }
  const { t } = i18n.global
  const name = engineLabel(engine)
  await confirmAction({
    title: t('databases.service.stopTitle', { name }),
    description: t('databases.service.stopText', { name }),
    confirmLabel: t('databases.service.stop'),
    destructive: true,
    run: async () => {
      await store.runJob(start, { rethrow: true })
    },
  })
}

/** Switches remote access (step-up protected); Redis refuses it without a password. */
export async function toggleRemote(engine: DbEngine): Promise<void> {
  const { t } = i18n.global
  const enable = !engine.remote
  if (enable && engine.kind === 'redis' && !engine.redis?.passwordSet) {
    notifyInfo(t('errors.redis.password_required'))
    return
  }
  const store = useDatabasesStore()
  await confirmAction({
    title: enable ? t('databases.remote.enableTitle') : t('databases.remote.disableTitle'),
    description: enable
      ? engine.kind === 'postgresql'
        ? t('databases.remote.enablePg')
        : t('databases.remote.enableRestart')
      : t('databases.remote.disable'),
    confirmLabel: enable ? t('databases.remote.enable') : t('databases.remote.localOnly'),
    run: async () => {
      // Step-up protected: the client asks for the password and retries.
      await store.runJob(() => setRemoteAccess(engine.id, enable), { rethrow: true })
    },
  })
}

export function dumpDatabase(engine: DbEngine, name?: string): Promise<Job | null> {
  return useDatabasesStore().runJob(() => createDump(engine.id, name))
}

/** Deleting a database asks for its name; its owner can go with it. */
export function askDropDatabase(engine: DbEngine, db: DbDatabase): Promise<boolean> {
  const { t } = i18n.global
  const store = useDatabasesStore()
  return confirmAction({
    title: t('databases.db.dropTitle', { name: db.name }),
    description: (withOwner) =>
      withOwner && db.owner
        ? `${t('databases.db.dropContent')} ${t('databases.db.dropOwnerText', { user: db.owner })}`
        : t('databases.db.dropContent'),
    confirmLabel: t('databases.db.drop'),
    destructive: true,
    typeToConfirm: db.name,
    option: db.owner ? { label: t('databases.db.dropOwner', { user: db.owner }) } : undefined,
    run: async (withOwner) => {
      // Step-up protected: the client asks for the password and retries.
      await store.runJob(() => dropDatabase(engine.id, db.name, withOwner && !!db.owner), { rethrow: true })
    },
  })
}

/** Deleting a user asks for its name. */
export function askDropUser(engine: DbEngine, user: DbUser): Promise<boolean> {
  const { t } = i18n.global
  const store = useDatabasesStore()
  return confirmAction({
    title: t('databases.user.dropTitle', { name: user.name }),
    description: t('databases.user.dropContent'),
    confirmLabel: t('databases.user.drop'),
    destructive: true,
    typeToConfirm: user.name,
    run: async () => {
      await store.runJob(() => dropDbUser(engine.id, user.name), { rethrow: true })
    },
  })
}

/** Downloads through a plain link after re-authenticating, so large dumps stream to disk. */
export async function downloadDump(engine: DbEngine, dump: DumpInfo): Promise<void> {
  try {
    await ensureElevated()
  } catch (err) {
    if (!isStepUpRequired(err)) notifyError(err)
    return
  }
  const link = document.createElement('a')
  link.href = dumpUrl(engine.id, dump.file)
  link.download = dump.file
  document.body.append(link)
  link.click()
  link.remove()
}

/** Deletes a dump file (step-up protected); resolves true once it is gone. */
export function askDeleteDump(engine: DbEngine, dump: DumpInfo): Promise<boolean> {
  const { t } = i18n.global
  return confirmAction({
    title: t('databases.dumps.deleteTitle', { file: dump.file }),
    description: t('databases.dumps.deleteContent'),
    confirmLabel: t('databases.dumps.delete'),
    destructive: true,
    run: async () => {
      await deleteDump(engine.id, dump.file)
      notifySuccess(t('databases.dumps.deleted', { file: dump.file }))
    },
  })
}
