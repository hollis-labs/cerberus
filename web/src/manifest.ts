import type { FrontendManifest } from "@hollis-labs/app-shell"
import generated from "./generated/frontend.manifest.json"

/** `substrate frontend` merges the modules' items with the app's own (cmd/cerberus/ui/app.json). */
export const manifest = generated as unknown as FrontendManifest
