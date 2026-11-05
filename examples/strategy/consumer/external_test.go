package consumer_test

import (
	"example.com/strategy/consumer"
	"testing"
)

func TestShadow(t *testing.T) {
	if consumer.Shadow() != 1 {
		t.Fatal("shadow")
	}
}
