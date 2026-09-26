// Passkey ceremonies for the approvals page (P3-4b). The daemon hands over
// WebAuthn options as JSON and verifies what comes back; the browser and the
// authenticator do the rest. User verification (Touch ID, a PIN) is required
// by the options, and the daemon checks the authenticator reported it.

// The daemon sends each ceremony's options as base64url of their JSON: they
// are public, and they travel opaque because the response redactor walks
// JSON by key and read WebAuthn's "allowCredentials" as a credential.
type OptionsJSON = { publicKey?: unknown } & Record<string, unknown>

function decodeOptions(encoded: string): OptionsJSON {
  const b64 = encoded.replace(/-/g, '+').replace(/_/g, '/')
  const bin = atob(b64 + '='.repeat((4 - (b64.length % 4)) % 4))
  return JSON.parse(new TextDecoder().decode(Uint8Array.from(bin, (c) => c.charCodeAt(0)))) as OptionsJSON
}

interface PublicKeyCredentialStatics {
  parseCreationOptionsFromJSON?: (options: unknown) => PublicKeyCredentialCreationOptions
  parseRequestOptionsFromJSON?: (options: unknown) => PublicKeyCredentialRequestOptions
}

interface JSONableCredential {
  toJSON?: () => unknown
}

function statics(): PublicKeyCredentialStatics {
  if (typeof window === 'undefined' || !('PublicKeyCredential' in window)) {
    throw new Error('This browser does not support passkeys.')
  }
  return window.PublicKeyCredential as unknown as PublicKeyCredentialStatics
}

function inner(options: OptionsJSON): unknown {
  return options.publicKey ?? options
}

function toJSON(credential: Credential | null): unknown {
  const c = credential as (Credential & JSONableCredential) | null
  if (!c) throw new Error('No passkey was used.')
  if (!c.toJSON) throw new Error('This browser cannot hand a passkey back as JSON; use a current Chrome, Safari or Firefox.')
  return c.toJSON()
}

// createPasskey runs a registration ceremony.
export async function createPasskey(options: string): Promise<unknown> {
  const parse = statics().parseCreationOptionsFromJSON
  if (!parse) throw new Error('This browser cannot read passkey options; use a current Chrome, Safari or Firefox.')
  return toJSON(await navigator.credentials.create({ publicKey: parse(inner(decodeOptions(options))) }))
}

// assertPasskey runs an authentication ceremony: sign the daemon's challenge
// with an enrolled passkey.
export async function assertPasskey(options: string): Promise<unknown> {
  const parse = statics().parseRequestOptionsFromJSON
  if (!parse) throw new Error('This browser cannot read passkey options; use a current Chrome, Safari or Firefox.')
  return toJSON(await navigator.credentials.get({ publicKey: parse(inner(decodeOptions(options))) }))
}
