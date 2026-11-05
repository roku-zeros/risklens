package main

import (
	"example.com/strategy/consumer"
	"example.com/strategy/refinance"
	"example.com/strategy/state"
)

// Evaluate chooses branches, joins their shared state, and overwrites earlier values.
func Evaluate(a *state.Application) {
	if a.Product == "consumer" {
		consumer.Calculate(a)
	} else {
		refinance.Calculate(a)
	}
	a.Limit = min(a.Limit, 100000)
	state.Finalize(a, consumer.Policy{Limit: 90000}, func(a *state.Application) { a.Limit -= 100 })
}
func main() {}
