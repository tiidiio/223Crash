package main

import (
	v1 "github.com/tiidiio/223Crash/contracts/product/v1"
	"github.com/tiidiio/223Crash/internal/product"
	"testing"
)

func TestSlotMapping(t *testing.T) {
	a, ok := product.SlotToPanel("A")
	if !ok || a != v1.PanelA {
		t.Fatalf("slot A")
	}
	b, ok := product.SlotToPanel("B")
	if !ok || b != v1.PanelB {
		t.Fatalf("slot B")
	}
	if product.PanelToSlot(v1.PanelA) != "A" {
		t.Fatalf("panel A")
	}
}
