import { Button, Input, Pill } from "@hollis-labs/design-components"
import { useIdentity } from "@hollis-labs/kit-identity/client"
import { useEffect, useState } from "react"
import { cerberus } from "../api/cerberus"
import { userRequired } from "../api/request"
import type { ApprovalInfo } from "../api/types"
import { Loadable, messageOf, Page, Section, useLoad } from "./common"

/**
 * Who may decide here. Approving is the owner's console identity session alone (decision 0.1): a
 * signed-in person whose scopes cover cerberus.approve. An API key or any other non-person caller
 * gets 403 user_required from the profile route; that hides the controls.
 */
export type Approver =
  | { state: "checking" }
  | { state: "person" }
  | { state: "not-person"; reason: string }

const APPROVE = "cerberus.approve"

export function useApprover(): Approver {
  const { client } = useIdentity()
  const [a, setA] = useState<Approver>({ state: "checking" })
  useEffect(() => {
    let current = true
    client
      .readProfile()
      .then((p) => {
        if (!current) return
        if (!p) {
          setA({ state: "not-person", reason: "You are signed out." })
        } else if (p.scopes.includes("*") || p.scopes.includes(APPROVE)) {
          setA({ state: "person" })
        } else {
          setA({ state: "not-person", reason: `Your account does not hold ${APPROVE}.` })
        }
      })
      .catch((e: unknown) => {
        if (!current) return
        const code = (e as { code?: string }).code
        setA({
          state: "not-person",
          reason:
            code === "user_required"
              ? "This session is not a signed-in person (an API key or a minted token): only the owner signed in to the console can approve."
              : messageOf(e),
        })
      })
    return () => {
      current = false
    }
  }, [client])
  return a
}

function statusTone(s: ApprovalInfo["status"]) {
  switch (s) {
    case "pending":
      return "warning" as const
    case "approved":
    case "consumed":
      return "success" as const
    case "denied":
    case "revoked":
      return "danger" as const
  }
  return undefined
}

/** The text approving must type: the target's resource, else its first field, else the operation. */
export function approvalTarget(a: ApprovalInfo): string {
  if (a.target.resource) return a.target.resource
  const fields = a.target.fields ?? {}
  const keys = Object.keys(fields).sort()
  if (keys.length > 0) return fields[keys[0]]
  return `${a.connector}.${a.operation}`
}

/** One approval with its decide and revoke controls, shown only to a person approver. */
export function ApprovalDetail({
  a,
  approver,
  onChanged,
}: {
  a: ApprovalInfo
  approver: Approver
  onChanged: () => void
}) {
  const [typed, setTyped] = useState("")
  const [reason, setReason] = useState("")
  const [result, setResult] = useState("")
  const act = (p: Promise<unknown>, done: string) => {
    setResult("")
    p.then(() => {
      setResult(done)
      onChanged()
    }).catch((e: unknown) =>
      setResult(
        userRequired(e)
          ? "Refused (403 user_required): only the owner signed in to the console can decide approvals."
          : messageOf(e),
      ),
    )
  }
  const target = approvalTarget(a)
  return (
    <section className="panel stack" aria-label={`Approval ${a.id}`}>
      <div>
        <Pill tone={statusTone(a.status)}>{a.status}</Pill>{" "}
        <span className="mono">
          {a.connector}.{a.operation}
        </span>{" "}
        on <span className="mono">{target}</span>
      </div>
      <div className="text-fg-muted">
        Asked by {a.principal.kind ?? "unknown"} via {a.principal.via ?? "unknown"}, expires{" "}
        {a.expires_at}
        {a.plan_hash ? (
          <>
            , plan <span className="mono">{a.plan_hash.slice(0, 12)}</span>
          </>
        ) : null}
      </div>
      {approver.state === "person" ? (
        <>
          {a.status === "pending" ? (
            <div className="stack">
              <Input
                aria-label="Type the target to approve"
                placeholder={target}
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
              />
              <Input
                aria-label="Reason"
                placeholder="why (recorded)"
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
              <div className="flex gap-2">
                <Button
                  disabled={typed !== target}
                  onClick={() =>
                    act(
                      cerberus.decideApproval(a.id, { approve: true, typed, reason }),
                      "Approved.",
                    )
                  }
                >
                  Approve
                </Button>
                <Button
                  variant="outline"
                  onClick={() =>
                    act(cerberus.decideApproval(a.id, { approve: false, typed, reason }), "Denied.")
                  }
                >
                  Deny
                </Button>
              </div>
            </div>
          ) : null}
          {a.status === "approved" ? (
            <Button
              variant="destructive"
              onClick={() => act(cerberus.revokeApproval(a.id, reason), "Revoked.")}
            >
              Revoke
            </Button>
          ) : null}
        </>
      ) : approver.state === "not-person" ? (
        <p role="note" className="text-fg-muted">
          Deciding is hidden: {approver.reason}
        </p>
      ) : null}
      {result ? <p role="status">{result}</p> : null}
    </section>
  )
}

/** The approvals list, the linked approval first (an approval link is /approvals?id=<id>). */
export function ApprovalsView({ approver }: { approver: Approver }) {
  const focus = new URLSearchParams(window.location.search).get("id") ?? ""
  const list = useLoad(() => cerberus.listApprovals(), 5_000)
  return (
    <Section title="Approvals">
      <Loadable load={list} what="approvals">
        {(r) => {
          const items = [...(r.approvals ?? [])].sort((x, y) =>
            x.id === focus ? -1 : y.id === focus ? 1 : 0,
          )
          return items.length === 0 ? (
            <p className="text-fg-muted">No approvals.</p>
          ) : (
            items.map((a) => (
              <ApprovalDetail key={a.id} a={a} approver={approver} onChanged={list.reload} />
            ))
          )
        }}
      </Loadable>
    </Section>
  )
}

export function ApprovalsPage() {
  const approver = useApprover()
  return (
    <Page>
      <ApprovalsView approver={approver} />
    </Page>
  )
}
