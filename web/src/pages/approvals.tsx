import { EmptyState } from "@hollis-labs/design-components"
import { Page } from "./common"

/**
 * The approvals page shell. An approval link (`/approvals?id=<approval>`) lands here after sign-in;
 * the old one-approval scoped session is gone (docs/substrate-port/console-api-contract.md).
 * The list and the decide and revoke actions arrive in slice 11b. Approving is the owner's signed-in
 * console session (decision 0.1: no passkey).
 */
export function ApprovalsPage() {
  const focus = new URLSearchParams(window.location.search).get("id") ?? ""
  return (
    <Page>
      <section className="panel stack" aria-labelledby="approvals-pending">
        <h2 id="approvals-pending" className="text-lg font-semibold">
          Approvals
        </h2>
        {focus ? (
          <p>
            Approval <span className="mono">{focus}</span> was linked.
          </p>
        ) : null}
        <EmptyState
          variant="empty"
          eyebrow="Not wired yet"
          title="Pending and recent approvals, and active grants."
          description="Slice 11b: GET /api/approvals, GET /api/approvals/{id}."
        />
      </section>
      <section className="panel stack" aria-labelledby="approvals-decide">
        <h2 id="approvals-decide" className="text-lg font-semibold">
          Decide
        </h2>
        <EmptyState
          variant="empty"
          eyebrow="Not wired yet"
          title="Approve or deny a gated plan from your signed-in session; revoke a grant."
          description="Slice 11b: POST /api/approvals/{id}/decide, /revoke (cerberus.approve, owner session only)."
        />
      </section>
    </Page>
  )
}
