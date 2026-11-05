// Package other deliberately shares a package name with no other path assumption.
package state

type Decision struct{ Limit string }

func ApplyIncomeCap() string { return "unrelated" }
