import { describe, expect, it } from "vitest"
import { ApiError, notAvailable } from "./request"

describe("notAvailable", () => {
  it("is true for a route this build does not serve yet", () => {
    expect(notAvailable(new ApiError("404 page not found", 404))).toBe(true)
    expect(notAvailable(new ApiError("not implemented", 501))).toBe(true)
    expect(notAvailable(new ApiError("unmapped", 403, "route_not_mapped"))).toBe(true)
  })

  it("is false for a served route's not_found, such as an unknown resource", () => {
    expect(notAvailable(new ApiError('resource "x" not found', 404, "not_found"))).toBe(false)
  })

  it("is false for other refusals and plain errors", () => {
    expect(notAvailable(new ApiError("forbidden", 403, "insufficient_scope"))).toBe(false)
    expect(notAvailable(new Error("network"))).toBe(false)
  })
})
