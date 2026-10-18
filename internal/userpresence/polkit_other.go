//go:build !linux

package userpresence

func NewPolkit() Verifier { return Refuse{Why: "polkit enrollment is supported on Linux only"} }
