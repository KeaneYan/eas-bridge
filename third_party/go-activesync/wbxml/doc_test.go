// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package wbxml

import (
	"reflect"
	"testing"
)

func TestE_Constructor(t *testing.T) {
	e := E(PageAirSync, "Sync",
		E(PageAirSync, "SyncKey", Text("0")),
	)
	if e.Codepage != PageAirSync || e.Name != "Sync" {
		t.Errorf("e = %+v", e)
	}
	if len(e.Children) != 1 {
		t.Fatalf("children: %d", len(e.Children))
	}
	c := e.Children[0].(*Element)
	if c.Name != "SyncKey" {
		t.Errorf("child: %q", c.Name)
	}
}

func TestTextContent_concatenatesText(t *testing.T) {
	e := &Element{
		Name: "x",
		Children: []Node{
			Text("a"),
			&Element{Name: "ignored", Children: []Node{Text("nope")}},
			Text("b"),
		},
	}
	if got := e.TextContent(); got != "ab" {
		t.Errorf("got %q, want %q", got, "ab")
	}
}

func TestTextContent_emptyOnNoText(t *testing.T) {
	e := &Element{Name: "x"}
	if got := e.TextContent(); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestFind_depthFirst(t *testing.T) {
	leaf := &Element{Name: "leaf"}
	root := &Element{
		Name: "root",
		Children: []Node{
			&Element{Name: "a", Children: []Node{leaf}},
			&Element{Name: "b"},
		},
	}
	if got := root.Find("leaf"); got != leaf {
		t.Errorf("Find returned %p, want %p", got, leaf)
	}
	if got := root.Find("nope"); got != nil {
		t.Errorf("Find unknown returned %v", got)
	}
}

func TestFind_returnsSelf(t *testing.T) {
	root := &Element{Name: "root"}
	if got := root.Find("root"); got != root {
		t.Error("Find should return self when name matches")
	}
}

func TestFind_nilSafe(t *testing.T) {
	var nilElem *Element
	if got := nilElem.Find("x"); got != nil {
		t.Errorf("Find on nil = %v", got)
	}
}

func TestFindAll_documentOrder(t *testing.T) {
	root := &Element{
		Name: "Root",
		Children: []Node{
			&Element{Name: "Item", Children: []Node{Text("1")}},
			&Element{Name: "Other", Children: []Node{
				&Element{Name: "Item", Children: []Node{Text("2")}},
			}},
			&Element{Name: "Item", Children: []Node{Text("3")}},
		},
	}
	got := root.FindAll("Item")
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	want := []string{"1", "2", "3"}
	values := []string{got[0].TextContent(), got[1].TextContent(), got[2].TextContent()}
	if !reflect.DeepEqual(values, want) {
		t.Errorf("values=%v want=%v", values, want)
	}
}

func TestFindAll_nilSafe(t *testing.T) {
	var nilElem *Element
	if got := nilElem.FindAll("x"); got != nil {
		t.Errorf("FindAll on nil = %v", got)
	}
}

func TestNodeInterfaceCompliance(t *testing.T) {
	// Compile-time check that each concrete type satisfies Node.
	var _ Node = &Element{}
	var _ Node = Text("")
	var _ Node = Opaque(nil)

	// Coverage tooling reports the marker isNode() bodies as 0% unless
	// they are invoked at runtime. Call each on the concrete type so
	// the cover tool sees the method entry execute.
	(&Element{}).isNode()
	Text("").isNode()
	Opaque(nil).isNode()
}
