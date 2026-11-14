// SPDX-License-Identifier: AGPL-3.0-only
//
// The panel's OpenAPI 3.1 description, with the subset of types the API
// reference page reads. Unknown fields are kept but not typed.
import { api } from './client'

export type JsonSchema = Record<string, unknown>

export interface MediaType {
  schema?: JsonSchema
}

export interface Operation {
  summary?: string
  description?: string
  tags?: string[]
  requestBody?: { content?: Record<string, MediaType> }
  responses?: Record<string, { description?: string; content?: Record<string, MediaType> }>
  /** Permission needed to call the operation; absent for public routes. */
  'x-cairn-permission'?: string
  /** True when the operation asks for re-authentication. */
  'x-cairn-step-up'?: boolean
}

export interface OpenApiDocument {
  openapi: string
  info?: { title?: string; version?: string }
  paths?: Record<string, Record<string, unknown>>
  components?: { schemas?: Record<string, JsonSchema> }
  tags?: Array<{ name: string; description?: string }>
}

export function fetchOpenApi(): Promise<OpenApiDocument> {
  return api.get<OpenApiDocument>('/openapi.json')
}
