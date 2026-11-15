import type { ApprovalRef } from "./types"

/**
 * Raised for any non-2xx answer. `code` and `details` come from the flat error body
 * `{code, message, details?}`; a gated write's refusal names its approval in `details.approval`.
 */
export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
    public code = "",
    public details: Record<string, unknown> = {},
  ) {
    super(message)
  }
}

/** Fired on a 401: the session ended. The identity gate returns to its sign-in screen. */
export const UNAUTHORIZED_EVENT = "cerberus:unauthorized"

type Query = Record<string, string | number | undefined>

/**
 * JSON over the same-origin cookie session. Every request carries X-Requested-With, which the
 * backend requires on cookie writes. An empty body (204) resolves to undefined. Sign-in, sign-out
 * and the identity screens are kit-identity's own.
 */
export async function request<T>(
  path: string,
  method = "GET",
  body?: unknown,
  query?: Query,
): Promise<T> {
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(query ?? {})) if (v !== undefined) qs.set(k, String(v))
  const search = qs.size > 0 ? `?${qs}` : ""
  const response = await fetch(`/api/${path}${search}`, {
    method,
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", "X-Requested-With": "XMLHttpRequest" },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await response.text()
  let data: { code?: string; message?: string; details?: Record<string, unknown> } | null = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = null
    }
  }
  if (!response.ok) {
    if (response.status === 401) window.dispatchEvent(new Event(UNAUTHORIZED_EVENT))
    throw new ApiError(
      data?.message || response.statusText || "The request could not be completed.",
      response.status,
      data?.code ?? "",
      data?.details ?? {},
    )
  }
  return data as T
}

/** The approval a refused write names, if any (codes approval_pending / approval_required). */
export function refusalApproval(err: unknown): ApprovalRef | null {
  if (!(err instanceof ApiError)) return null
  const ref = err.details.approval as ApprovalRef | undefined
  return ref?.id ? ref : null
}

/** A refusal the console's confirm step can meet in place: policy wants the call confirmed. */
export function confirmableApproval(err: unknown): ApprovalRef | null {
  const ref = refusalApproval(err)
  return ref && ref.channel === "tty_confirm" ? ref : null
}

/**
 * True when the route's backend has not landed in this build yet: the guard refuses a route it
 * does not map (403 route_not_mapped), or the route answers 404 or 501. Pages show "not available
 * yet" for these, never mock data.
 */
export function notAvailable(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false
  return (
    err.status === 404 ||
    err.status === 501 ||
    (err.status === 403 && err.code === "route_not_mapped")
  )
}

/** True when the caller is not a signed-in person: the person-only routes answer 403 user_required. */
export function userRequired(err: unknown): boolean {
  return err instanceof ApiError && err.status === 403 && err.code === "user_required"
}
