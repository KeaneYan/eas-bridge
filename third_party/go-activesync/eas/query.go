// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"

	"github.com/hstern/go-activesync/wbxml"
)

// Query is the AST for a structured EAS Search/Find query. Build trees
// from the constructors below; call EncodeQuery to turn them into the
// WBXML element placed inside <Query>.
//
// The Search command's <Query> can hold a single child (FreeText, And,
// Or, EqualTo, etc.). Most callers want And + leaf comparisons.
type Query interface {
	encode() *wbxml.Element
}

// FreeText matches any item containing the given substring.
func FreeText(s string) Query { return queryFreeText(s) }

type queryFreeText string

func (q queryFreeText) encode() *wbxml.Element {
	return wbxml.E(wbxml.PageSearch, "FreeText", wbxml.Text(string(q)))
}

// And combines sub-queries with logical AND.
func And(qs ...Query) Query { return queryAnd(qs) }

type queryAnd []Query

func (q queryAnd) encode() *wbxml.Element {
	out := wbxml.E(wbxml.PageSearch, "And")
	for _, sub := range q {
		out.Children = append(out.Children, sub.encode())
	}
	return out
}

// Or combines sub-queries with logical OR.
func Or(qs ...Query) Query { return queryOr(qs) }

type queryOr []Query

func (q queryOr) encode() *wbxml.Element {
	out := wbxml.E(wbxml.PageSearch, "Or")
	for _, sub := range q {
		out.Children = append(out.Children, sub.encode())
	}
	return out
}

// SearchProp identifies a property by codepage + element name.
// Helpers below provide pre-built ones for common email/contact fields.
type SearchProp struct {
	Page byte
	Name string
}

// EqualTo asserts that property equals value.
func EqualTo(p SearchProp, value string) Query {
	return &queryComparison{op: "EqualTo", prop: p, value: value}
}

// GreaterThan asserts property > value.
func GreaterThan(p SearchProp, value string) Query {
	return &queryComparison{op: "GreaterThan", prop: p, value: value}
}

// LessThan asserts property < value.
func LessThan(p SearchProp, value string) Query {
	return &queryComparison{op: "LessThan", prop: p, value: value}
}

type queryComparison struct {
	op    string
	prop  SearchProp
	value string
}

func (q *queryComparison) encode() *wbxml.Element {
	return wbxml.E(wbxml.PageSearch, q.op,
		wbxml.E(q.prop.Page, q.prop.Name),
		wbxml.E(wbxml.PageSearch, "Value", wbxml.Text(q.value)),
	)
}

// CollectionID restricts search to a specific folder. Convenience
// wrapper around an EqualTo against AirSync.CollectionId.
func CollectionID(id string) Query {
	return EqualTo(SearchProp{Page: wbxml.PageAirSync, Name: "CollectionId"}, id)
}

// EmailClass restricts the search to email items.
func EmailClass() Query {
	return EqualTo(SearchProp{Page: wbxml.PageAirSync, Name: "Class"}, "Email")
}

// Pre-built property handles for common email comparisons.
var (
	PropEmailFrom         = SearchProp{Page: wbxml.PageEmail, Name: "From"}
	PropEmailTo           = SearchProp{Page: wbxml.PageEmail, Name: "To"}
	PropEmailSubject      = SearchProp{Page: wbxml.PageEmail, Name: "Subject"}
	PropEmailDateReceived = SearchProp{Page: wbxml.PageEmail, Name: "DateReceived"}
	PropEmailHasAttach    = SearchProp{Page: wbxml.PageAirSyncBase, Name: "Attachments"}
	PropConvoID           = SearchProp{Page: wbxml.PageEmail2, Name: "ConversationId"}
)

// SearchEmailQuery issues a Search command with an arbitrary structured
// query. Falls through to the same Search implementation as SearchEmail
// but skips the "And + Class=Email + FreeText" wrapper so callers can
// build whatever shape they want.
func (c *httpClient) SearchEmailQuery(ctx context.Context, q Query, opts EmailSearchOptions) (*EmailSearchResult, error) {
	return c.searchStructured(ctx, q, opts)
}
