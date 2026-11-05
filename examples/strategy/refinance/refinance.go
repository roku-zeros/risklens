package refinance

import "example.com/strategy/state"

type Policy struct{ Limit int }

// Calculate computes a refinance offer. Рефинансирование: погашаем старый долг и рассчитываем доступный остаток.
func Calculate(a *state.Application)          { a.Limit = a.Income*6 - a.Debt; state.ApplyIncomeCap(a) }
func (p *Policy) Adjust(a *state.Application) { a.Limit = min(a.Limit, p.Limit) }
