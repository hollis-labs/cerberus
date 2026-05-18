import { createApiClient, type JsonObject } from '@hollis-labs/sysop-ui'

const http = createApiClient({ baseUrl: '' })

export interface SessionInfo {
  action_token: string
}

export interface ResourceInfo {
  id: string
  name: string
  type: string
  project: string
  connector: string
  mode?: string
  supervisor?: string
  run_from?: string
  url?: string
  port?: number
  has_build?: boolean
  status?: string
  operator_stopped?: boolean
  artifact_installed?: boolean
  artifact_stale?: boolean
  recommended_action?: string
  recommended_next_step?: string
  tags?: string[]
}

export interface ResourceRuntimeStatus extends ResourceInfo {
  status: string
  service_name?: string
  artifact_path?: string
  install_root?: string
  artifact_stale_reason?: string
  artifact_source?: string
  artifact_synced_at?: string
  recommended_reason?: string
  launchd_loaded?: boolean
  launchd_state?: string
  launchd_pid?: number
  launchd_last_exit_code?: number | null
  launchd_throttled?: boolean
  launchd_reason?: string
  launchd_diagnosis?: string
  launchd_highlights?: string[]
}

export interface LogLines {
  resource_id?: string
  stream?: string
  content: string
  log_path: string
}

export interface OpResult {
  success: boolean
  service_id: string
  message?: string
  build_output?: string
  install_output?: string
  install_skipped?: boolean
  error?: string
}

export type ResourceAction = 'apply' | 'deploy' | 'reload' | 'stop'

export const apiClient = {
  getSession: (signal?: AbortSignal) => http.get<SessionInfo>('/api/session', { signal }),
  listResources: (signal?: AbortSignal) => http.get<ResourceInfo[]>('/api/resources', { signal }),
  getResource: (id: string, signal?: AbortSignal) => http.get<ResourceRuntimeStatus>(`/api/resources/${encodeURIComponent(id)}`, { signal }),
  getLogs: (id: string, stream: string, lines = 120, signal?: AbortSignal) =>
    http.get<LogLines>(`/api/resources/${encodeURIComponent(id)}/logs`, {
      signal,
      query: { stream, lines },
    }),
  runResourceAction: (id: string, action: ResourceAction, token: string) =>
    http.post<OpResult>(`/api/resources/${encodeURIComponent(id)}/${action}`, {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
}
