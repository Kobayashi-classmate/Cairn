// SPDX-License-Identifier: AGPL-3.0-only
//
// The editable fields of a site, their checks and the request they make.
// Shared by the "New site" sheet and the sub-items of the settings drawer:
// every change sends the whole site (PUT /sites/{code}), so a sub-item starts
// from formFromSite and changes only its own fields. The checks mirror the
// server's rules for faster feedback; the server checks everything again.
import type { CertView } from '@/api/certs'
import type { AppRuntime, AppSpec, EnvVar, SiteRequest, SiteType, SiteView } from '@/api/sites'
import { certCovers } from '@/certs/labels'
import { i18n } from '@/i18n'
import { validationMessage } from '@/i18n/errors'
import {
  MAX_DOMAINS,
  MAX_ENV,
  MAX_INDEX,
  MAX_WRITABLE,
  isAppCommand,
  isEnvName,
  isEnvValue,
  isHealthPath,
  isIndexName,
  isProxyUrl,
  isRelativePath,
  isSiteCode,
  isSiteHost,
} from './validate'

export interface AppForm {
  /** "<software>@<line>", or "" for system tools only. */
  runtime: string
  command: string
  workDir: string
  /** Text of the number fields; "" means automatic / no limit. */
  port: string
  env: EnvVar[]
  memoryMb: string
  cpuPercent: string
  healthPath: string
  webSocket: boolean
}

export interface SiteForm {
  type: SiteType
  code: string
  domains: string[]
  root: string
  index: string[]
  /** Id of a built-in rule, "" for none. */
  rewrite: string
  php: string
  writable: string[]
  proxyUrl: string
  webSocket: boolean
  upstreamHost: boolean
  /** Certificate id, "" for HTTP only. */
  tlsCert: string
  tlsRedirect: boolean
  tlsHsts: boolean
  app: AppForm
}

/** Field names of SiteForm checks; the keys of FieldErrors. */
export type SiteField =
  | 'domains'
  | 'code'
  | 'root'
  | 'index'
  | 'php'
  | 'writable'
  | 'proxyUrl'
  | 'tlsCert'
  | 'app.command'
  | 'app.workDir'
  | 'app.port'
  | 'app.env'
  | 'app.memoryMb'
  | 'app.cpuPercent'
  | 'app.healthPath'

export type FieldErrors = Partial<Record<SiteField, string>>

function emptyApp(): AppForm {
  return {
    runtime: '',
    command: '',
    workDir: '',
    port: '',
    env: [],
    memoryMb: '',
    cpuPercent: '',
    healthPath: '',
    webSocket: false,
  }
}

function numberText(value: number | undefined): string {
  return value ? String(value) : ''
}

export function emptySiteForm(type: SiteType = 'static', php = ''): SiteForm {
  return {
    type,
    code: '',
    domains: [],
    root: '',
    index: [],
    rewrite: '',
    php,
    writable: [],
    proxyUrl: '',
    webSocket: false,
    upstreamHost: false,
    tlsCert: '',
    tlsRedirect: true,
    tlsHsts: false,
    app: emptyApp(),
  }
}

/** The form for an existing site, exactly as it is specified. */
export function formFromSite(site: SiteView): SiteForm {
  const app = site.app
  return {
    type: site.type,
    code: site.code,
    domains: [...site.domains],
    root: site.root ?? '',
    index: [...(site.index ?? [])],
    rewrite: site.rewrite ?? '',
    php: site.php ?? '',
    writable: [...(site.writable ?? [])],
    proxyUrl: site.proxy?.url ?? '',
    webSocket: site.proxy?.webSocket === true,
    upstreamHost: site.proxy?.upstreamHost === true,
    tlsCert: site.tls?.cert ?? '',
    tlsRedirect: site.tls ? site.tls.redirect === true : true,
    tlsHsts: site.tls?.hsts === true,
    app: app
      ? {
          runtime: app.runtime && app.line ? `${app.runtime}@${app.line}` : '',
          command: app.command,
          workDir: app.workDir ?? '',
          port: numberText(app.port),
          env: (app.env ?? []).map((variable) => ({ ...variable })),
          memoryMb: numberText(app.memoryMb),
          cpuPercent: numberText(app.cpuPercent),
          healthPath: app.healthPath ?? '',
          webSocket: app.webSocket === true,
        }
      : emptyApp(),
  }
}

/** A whole number from a field, null when empty, NaN when not a whole number. */
export function parseCount(text: string): number | null {
  const trimmed = text.trim()
  if (trimmed === '') return null
  return /^\d+$/.test(trimmed) ? Number(trimmed) : Number.NaN
}

/** Static and PHP sites serve files: web root, index files and rewrite rules. */
export function servesFiles(type: SiteType): boolean {
  return type === 'static' || type === 'php'
}

/** Sites whose runtime user may write some directories. */
export function hasWritable(type: SiteType): boolean {
  return type === 'php' || type === 'app'
}

/** Checks a list of domains; null when it is fine. */
export function domainsError(domains: readonly string[]): string | null {
  if (domains.length === 0) return validationMessage('domains', 'required')
  if (domains.length > MAX_DOMAINS) return validationMessage('domains', 'count')
  if (!domains.every(isSiteHost)) return validationMessage('domains', 'host')
  return null
}

/**
 * Checks the fields of a form; with `only`, just those (a drawer sub-item
 * checks its own fields). `certs` are the valid certificates the user can
 * see; null when certificates are not available to the user.
 */
export function validateSiteForm(
  form: SiteForm,
  mode: 'create' | 'edit',
  certs: readonly CertView[] | null,
  only?: readonly SiteField[],
): FieldErrors {
  const errors: FieldErrors = {}
  const want = (field: SiteField) => !only || only.includes(field)
  const set = (field: SiteField, message: string | null) => {
    if (message && want(field)) errors[field] = message
  }

  set('domains', domainsError(form.domains))
  if (mode === 'create' && form.code.trim() && !isSiteCode(form.code.trim())) {
    set('code', validationMessage('siteCode', 'site_code'))
  }
  if (form.tlsCert && certs) {
    const cert = certs.find((c) => c.id === form.tlsCert)
    // A certificate that is not listed may have expired; the server decides.
    if (cert && !certCovers(cert, form.domains)) set('tlsCert', i18n.global.t('sites.form.certNotCovering'))
  }

  if (form.type === 'proxy') {
    const url = form.proxyUrl.trim()
    set('proxyUrl', url === '' ? validationMessage('proxy', 'required') : isProxyUrl(url) ? null : validationMessage('proxy', 'url'))
  }

  if (servesFiles(form.type)) {
    if (!isRelativePath(form.root.trim())) set('root', validationMessage('root', 'path'))
    if (form.index.length > MAX_INDEX) set('index', validationMessage('index', 'count'))
    else if (!form.index.every(isIndexName)) set('index', validationMessage('index', 'file_name'))
  }
  if (form.type === 'php' && !form.php) set('php', validationMessage('php', 'required'))

  if (hasWritable(form.type)) {
    if (form.writable.length > MAX_WRITABLE) set('writable', validationMessage('writable', 'count'))
    else if (!form.writable.every((path) => path !== '' && isRelativePath(path))) {
      set('writable', validationMessage('writable', 'path'))
    }
  }

  if (form.type === 'app') {
    const app = form.app
    const command = app.command.trim()
    set(
      'app.command',
      command === '' ? validationMessage('app.command', 'required') : isAppCommand(command) ? null : validationMessage('app.command', 'command'),
    )
    if (!isRelativePath(app.workDir.trim())) set('app.workDir', validationMessage('app.workDir', 'path'))
    const port = parseCount(app.port)
    if (port !== null && !(port >= 1024 && port <= 65535)) set('app.port', validationMessage('app.port', 'port'))
    const names = app.env.map((variable) => variable.name.trim())
    if (app.env.length > MAX_ENV || !names.every(isEnvName) || new Set(names).size !== names.length) {
      set('app.env', validationMessage('app.env', 'name'))
    } else if (!app.env.every((variable) => isEnvValue(variable.value))) {
      set('app.env', validationMessage('app.env', 'value'))
    }
    const memory = parseCount(app.memoryMb)
    if (memory !== null && memory !== 0 && !(memory >= 16)) set('app.memoryMb', validationMessage('app.memoryMb', 'range'))
    const cpu = parseCount(app.cpuPercent)
    if (cpu !== null && cpu !== 0 && !(cpu >= 5)) set('app.cpuPercent', validationMessage('app.cpuPercent', 'range'))
    const health = app.healthPath.trim()
    if (health && !isHealthPath(health)) set('app.healthPath', validationMessage('app.healthPath', 'path'))
  }
  return errors
}

/** The request for a checked form. On edit, the type and code are fixed and not sent. */
export function siteRequest(form: SiteForm, mode: 'create' | 'edit'): SiteRequest {
  const request: SiteRequest = { type: form.type, domains: [...form.domains] }
  if (mode === 'create' && form.code.trim()) request.code = form.code.trim()
  if (form.tlsCert) {
    request.tls = { cert: form.tlsCert }
    if (form.tlsRedirect) request.tls.redirect = true
    if (form.tlsHsts) request.tls.hsts = true
  }
  if (form.type === 'proxy') {
    request.proxy = { url: form.proxyUrl.trim() }
    if (form.webSocket) request.proxy.webSocket = true
    if (form.upstreamHost) request.proxy.upstreamHost = true
    return request
  }
  if (form.type === 'app') {
    const source = form.app
    const [runtime, line] = source.runtime ? source.runtime.split('@') : []
    // Port 0: on create the panel picks a free one; on update the current one stays.
    const app: AppSpec = { command: source.command.trim(), port: parseCount(source.port) ?? 0 }
    if (runtime && line) {
      app.runtime = runtime as AppRuntime
      app.line = line
    }
    if (source.workDir.trim()) app.workDir = source.workDir.trim()
    if (source.env.length) app.env = source.env.map((variable) => ({ name: variable.name.trim(), value: variable.value }))
    const memory = parseCount(source.memoryMb)
    if (memory) app.memoryMb = memory
    const cpu = parseCount(source.cpuPercent)
    if (cpu) app.cpuPercent = cpu
    if (source.healthPath.trim()) app.healthPath = source.healthPath.trim()
    if (source.webSocket) app.webSocket = true
    request.app = app
    if (form.writable.length) request.writable = [...form.writable]
    return request
  }
  if (form.root.trim()) request.root = form.root.trim()
  if (form.index.length) request.index = [...form.index]
  if (form.rewrite) request.rewrite = form.rewrite
  if (form.type === 'php') {
    request.php = form.php || undefined
    if (form.writable.length) request.writable = [...form.writable]
  }
  return request
}

/** Certificates offered for a site: valid ones that cover every domain. */
export function coveringCerts(certs: readonly CertView[] | null, domains: readonly string[]): CertView[] {
  if (!certs || domains.length === 0) return []
  return certs.filter((cert) => certCovers(cert, domains))
}
