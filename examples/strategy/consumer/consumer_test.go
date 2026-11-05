package consumer

import (
	"example.com/strategy/state"
	"testing"
)

func TestCalculate(t *testing.T) {
	a := &state.Application{Income: 100}
	Calculate(a)
	if a.Limit != 400 {
		t.Fatal(a.Limit)
	}
}
