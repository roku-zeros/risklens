// Package state holds shared mutable application data.
package state

type Decision struct {
	Limit int
	Rate  float64
	Term  int
	Codes []string
}
type Application struct {
	Decision // promoted fields must resolve to Decision, not Application.
	Income   int
	Debt     int
	Product  string
}

// ApplyIncomeCap is shared by products. Ограничение лимита по доходу клиента.
func ApplyIncomeCap(a *Application) {
	cap := a.Income * 4
	if a.Limit > cap {
		a.Limit = cap
	}
}

type Adjuster interface{ Adjust(*Application) }

// Finalize executes common adjustment then a dynamic product override.
func Finalize(a *Application, rule Adjuster, callback func(*Application)) {
	ApplyIncomeCap(a)
	rule.Adjust(a)
	callback(a)
}

// AccessKinds demonstrates reads, writes, compound updates and address taking.
func AccessKinds(a *Application) int {
	x := a.Limit
	a.Limit = x + 10
	a.Limit += 5
	a.Limit++
	a.Limit--
	alias := &a.Limit
	*alias = 123                // deliberately outside field-write coverage.
	copy := Decision{Limit: 42} // composite literal is not a selector access.
	_ = copy
	return a.Limit
}
