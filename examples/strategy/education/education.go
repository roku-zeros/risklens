package education

import "example.com/strategy/state"

// StudyGrace sets a study repayment grace period. Образовательный кредит: льготный период и срок обучения.
func StudyGrace(a *state.Application, years int) { a.Term = years*12 + 9; a.Rate = 3.0 }
