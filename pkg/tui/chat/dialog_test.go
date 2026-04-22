package chat

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

type stubDialog struct {
	id string
}

func (d *stubDialog) ID() string                    { return d.id }
func (d *stubDialog) HandleMsg(tea.Msg) Action      { return ActionClose{} }
func (d *stubDialog) View(width, height int) string { return "stub" }

func TestOverlay_OpenClose(t *testing.T) {
	o := NewOverlay()
	if o.HasDialogs() {
		t.Fatal("new overlay should be empty")
	}

	o.OpenDialog(&stubDialog{id: "a"})
	if !o.HasDialogs() {
		t.Fatal("should have dialog after open")
	}
	if f := o.Front(); f == nil || f.ID() != "a" {
		t.Fatalf("front should be 'a', got %v", f)
	}

	o.OpenDialog(&stubDialog{id: "b"})
	if f := o.Front(); f.ID() != "b" {
		t.Fatalf("front should be 'b', got %s", f.ID())
	}

	o.CloseFrontDialog()
	if f := o.Front(); f.ID() != "a" {
		t.Fatalf("front should be 'a' after closing b, got %s", f.ID())
	}

	o.CloseFrontDialog()
	if o.HasDialogs() {
		t.Fatal("should be empty after closing all")
	}
}

func TestOverlay_CloseByID(t *testing.T) {
	o := NewOverlay()
	o.OpenDialog(&stubDialog{id: "x"})
	o.OpenDialog(&stubDialog{id: "y"})

	o.CloseDialog("x")
	if o.Front().ID() != "y" {
		t.Fatalf("front should be 'y', got %s", o.Front().ID())
	}
	if o.ContainsDialog("x") {
		t.Fatal("should not contain 'x' after removal")
	}
}

func TestOverlay_HandleMsg(t *testing.T) {
	o := NewOverlay()
	if action := o.HandleMsg(nil); action != nil {
		t.Fatal("empty overlay should return nil action")
	}

	o.OpenDialog(&stubDialog{id: "d"})
	action := o.HandleMsg(nil)
	if _, ok := action.(ActionClose); !ok {
		t.Fatalf("stub dialog should return ActionClose, got %T", action)
	}
}

func TestOverlay_ContainsDialog(t *testing.T) {
	o := NewOverlay()
	o.OpenDialog(&stubDialog{id: "test"})
	if !o.ContainsDialog("test") {
		t.Fatal("should contain 'test'")
	}
	if o.ContainsDialog("other") {
		t.Fatal("should not contain 'other'")
	}
}
