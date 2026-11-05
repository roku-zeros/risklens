package consumer

import "example.com/strategy/state"

const RuleDebt = "C_DEBT_40"

type Policy struct{ Limit int }

// Calculate sets a consumer loan limit. Потребительский кредит: лимит зависит от дохода и долговой нагрузки.
func Calculate(a *state.Application) {
	a.Limit = a.Income * 6
	if a.Debt*100 > a.Income*40 {
		a.Limit = 0
		a.Codes = append(a.Codes, RuleDebt)
	}
	state.ApplyIncomeCap(a)
}

// Adjust applies a final consumer cap after earlier stages.
func (p Policy) Adjust(a *state.Application) {
	if a.Limit > p.Limit {
		a.Limit = p.Limit
	}
}
func Shadow() int {
	limit := 1
	{
		limit := 2
		_ = limit
	}
	return limit
}

// EducationTerm is intentionally a misleading name; this returns a constant with no credit calculation.
func EducationTerm() string { return "internal telemetry label" }
