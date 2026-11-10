package main

import (
	"testing"
	v1 "github.com/tiidiio/223Gaming/contracts/product/v1"
	"github.com/tiidiio/223Gaming/internal/product"
)

func TestSlotMapping(t *testing.T) {
	a, ok := product.SlotToPanel("A")
	if !ok || a != v1.PanelA { t.Fatalf("slot A") }
	b, ok := product.SlotToPanel("B")
	if !ok || b != v1.PanelB { t.Fatalf("slot B") }
	if product.PanelToSlot(v1.PanelA) != "A" { t.Fatalf("panel A") }
}
