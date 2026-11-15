import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { ApiError } from "../api/request"
import type { ApprovalInfo } from "../api/types"

const decideApproval = vi.fn()
vi.mock("../api/cerberus", () => ({
  cerberus: {
    decideApproval: (...a: unknown[]) => decideApproval(...a),
    revokeApproval: vi.fn(),
    listApprovals: vi.fn(),
  },
}))

const readProfile = vi.fn()
vi.mock("@hollis-labs/kit-identity/client", () => ({
  useIdentity: () => ({ client: identityClient }),
}))
const identityClient = { readProfile: () => readProfile() }

const { ApprovalDetail, useApprover } = await import("./approvals")

const pending: ApprovalInfo = {
  id: "apr_1",
  status: "pending",
  created_at: "2026-11-15T00:00:00Z",
  expires_at: "2026-11-15T01:00:00Z",
  principal: { kind: "agent", via: "api" },
  connector: "systemd",
  operation: "restart",
  target: { resource: "web-1" },
  args_digest: "d",
  channel: "out_of_band",
  scope: "once",
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe("ApprovalDetail", () => {
  it("shows the decide controls to a person session", () => {
    render(<ApprovalDetail a={pending} approver={{ state: "person" }} onChanged={() => {}} />)
    expect(screen.getByRole("button", { name: "Approve" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "Deny" })).toBeTruthy()
  })

  it("hides the decide controls when the session is not a person", () => {
    render(
      <ApprovalDetail
        a={pending}
        approver={{ state: "not-person", reason: "This session is not a signed-in person." }}
        onChanged={() => {}}
      />,
    )
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Deny" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Revoke" })).toBeNull()
    expect(screen.getByRole("note").textContent).toContain("Deciding is hidden")
  })

  it("hides the controls while the session is still being checked", () => {
    render(<ApprovalDetail a={pending} approver={{ state: "checking" }} onChanged={() => {}} />)
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull()
  })

  it("approves only once the target is typed", () => {
    render(<ApprovalDetail a={pending} approver={{ state: "person" }} onChanged={() => {}} />)
    const approve = screen.getByRole("button", { name: "Approve" }) as HTMLButtonElement
    expect(approve.disabled).toBe(true)
    fireEvent.change(screen.getByLabelText("Type the target to approve"), {
      target: { value: "web-1" },
    })
    expect(approve.disabled).toBe(false)
  })

  it("shows a 403 user_required refusal clearly", async () => {
    decideApproval.mockRejectedValue(
      new ApiError("approving needs a signed-in user", 403, "user_required"),
    )
    render(<ApprovalDetail a={pending} approver={{ state: "person" }} onChanged={() => {}} />)
    fireEvent.click(screen.getByRole("button", { name: "Deny" }))
    await waitFor(() =>
      expect(screen.getByRole("status").textContent).toBe(
        "Refused (403 user_required): only the owner signed in to the console can decide approvals.",
      ),
    )
  })
})

function Probe() {
  const a = useApprover()
  return (
    <p data-testid="approver">{a.state === "not-person" ? `${a.state}: ${a.reason}` : a.state}</p>
  )
}

describe("useApprover", () => {
  const seen = async () => {
    render(<Probe />)
    await act(async () => {})
    return screen.getByTestId("approver").textContent ?? ""
  }

  it("is a person when the profile holds cerberus.approve", async () => {
    readProfile.mockResolvedValue({ scopes: ["cerberus.read", "cerberus.approve"] })
    expect(await seen()).toBe("person")
  })

  it("is not a person on the profile route's 403 user_required", async () => {
    readProfile.mockRejectedValue(
      Object.assign(new Error("user required"), { code: "user_required" }),
    )
    expect(await seen()).toMatch(/^not-person: This session is not a signed-in person/)
  })

  it("is not a person without cerberus.approve", async () => {
    readProfile.mockResolvedValue({ scopes: ["cerberus.read"] })
    expect(await seen()).toBe("not-person: Your account does not hold cerberus.approve.")
  })

  it("is not a person when signed out", async () => {
    readProfile.mockResolvedValue(null)
    expect(await seen()).toBe("not-person: You are signed out.")
  })
})
