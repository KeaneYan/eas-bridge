// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/wbxml"
)

// EmptyFolderContents removes every item in folderID. If
// deleteSubfolders is true, sub-folders are removed too. The folder
// itself is preserved.
//
// Useful for "empty trash" workflows.
func (c *httpClient) EmptyFolderContents(ctx context.Context, folderID string, deleteSubfolders bool) error {
	if folderID == "" {
		return errors.New("eas: EmptyFolderContents: folderID is required")
	}
	op := wbxml.E(wbxml.PageItemOperations, "EmptyFolderContents",
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folderID)),
		wbxml.E(wbxml.PageItemOperations, "Options",
			wbxml.E(wbxml.PageItemOperations, "DeleteSubFolders",
				wbxml.Text(boolNumString(deleteSubfolders))),
		),
	)
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations", op),
	}
	resp, err := c.post(ctx, "ItemOperations", doc)
	if err != nil {
		return err
	}
	return checkItemOperationsStatus(resp, "ItemOperations/EmptyFolderContents")
}

// MoveViaItemOperations moves a single item using the ItemOperations
// container (an alternative to MoveItems with extra options like
// MoveAlways and the resulting destination ConversationId).
func (c *httpClient) MoveViaItemOperations(ctx context.Context, srcFolder, srcID, dstFolder string, moveAlways bool) (string, error) {
	if srcFolder == "" || srcID == "" || dstFolder == "" {
		return "", errors.New("eas: MoveViaItemOperations: srcFolder/srcID/dstFolder are required")
	}
	move := wbxml.E(wbxml.PageItemOperations, "Move",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(srcID)),
		wbxml.E(wbxml.PageItemOperations, "DstFldId", wbxml.Text(dstFolder)),
	)
	if moveAlways {
		move.Children = append(move.Children, wbxml.E(wbxml.PageItemOperations, "MoveAlways"))
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations", move),
	}
	resp, err := c.post(ctx, "ItemOperations", doc)
	if err != nil {
		return "", err
	}
	if err := checkItemOperationsStatus(resp, "ItemOperations/Move"); err != nil {
		return "", err
	}
	if r := resp.Root.Find("Response"); r != nil {
		if move := r.Find("Move"); move != nil {
			if id := move.Find("ServerId"); id != nil {
				return id.TextContent(), nil
			}
		}
	}
	return "", nil
}

// FetchDocumentLibrary retrieves a file from a SharePoint or UNC
// document library exposed through EAS. linkID is the
// document-library link identifier (typically discovered via Search
// with Store=DocumentLibrary).
//
// rangeStart and rangeEnd select a byte range within the file (set
// rangeEnd=0 to fetch from rangeStart to end). The returned data is
// the file contents.
func (c *httpClient) FetchDocumentLibrary(ctx context.Context, linkID string, rangeStart, rangeEnd int64) ([]byte, error) {
	if linkID == "" {
		return nil, errors.New("eas: FetchDocumentLibrary: linkID is required")
	}
	fetch := wbxml.E(wbxml.PageItemOperations, "Fetch",
		wbxml.E(wbxml.PageItemOperations, "Store", wbxml.Text("DocumentLibrary")),
		wbxml.E(wbxml.PageDocumentLibrary, "LinkId", wbxml.Text(linkID)),
	)
	options := wbxml.E(wbxml.PageItemOperations, "Options")
	if rangeStart > 0 || rangeEnd > 0 {
		// EAS Range is "<start>-<end>" inclusive byte offsets.
		rng := itoa64(rangeStart) + "-"
		if rangeEnd > 0 {
			rng += itoa64(rangeEnd)
		}
		options.Children = append(options.Children, wbxml.E(wbxml.PageItemOperations, "Range", wbxml.Text(rng)))
	}
	if len(options.Children) > 0 {
		fetch.Children = append(fetch.Children, options)
	}

	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations", fetch),
	}
	resp, err := c.post(ctx, "ItemOperations", doc)
	if err != nil {
		return nil, err
	}
	if err := checkItemOperationsStatus(resp, "ItemOperations/DocumentLibrary"); err != nil {
		return nil, err
	}
	r := resp.Root.Find("Response")
	if r == nil {
		return nil, errors.New("eas: FetchDocumentLibrary: missing Response")
	}
	if f := r.Find("Fetch"); f != nil {
		if props := f.Find("Properties"); props != nil {
			if data := props.Find("Data"); data != nil {
				if op := firstOpaque(data); op != nil {
					return op, nil
				}
				return []byte(data.TextContent()), nil
			}
		}
	}
	return nil, errors.New("eas: FetchDocumentLibrary: no data in response")
}

// FetchAttachmentResult holds the bytes and metadata of one attachment.
type FetchAttachmentResult struct {
	// Data is the attachment payload (already gunzipped if Range was
	// not used).
	Data []byte
	// ContentType is the MIME type the server reported, when available.
	ContentType string
	// Range is the server-echoed byte range when partial fetch was
	// requested. Empty otherwise.
	Range string
}

// FetchAttachment retrieves a single attachment by its EAS
// FileReference (typically read from EmailItem.Attachments[i].FileReference
// after a Sync or ItemOperations Fetch). rangeStart/rangeEnd allow
// resumable transfers; pass 0/0 for the full attachment.
func (c *httpClient) FetchAttachment(ctx context.Context, fileReference string, rangeStart, rangeEnd int64) (*FetchAttachmentResult, error) {
	if fileReference == "" {
		return nil, errors.New("eas: FetchAttachment: fileReference is required")
	}
	fetch := wbxml.E(wbxml.PageItemOperations, "Fetch",
		wbxml.E(wbxml.PageItemOperations, "Store", wbxml.Text("Mailbox")),
		wbxml.E(wbxml.PageAirSyncBase, "FileReference", wbxml.Text(fileReference)),
	)
	if rangeStart > 0 || rangeEnd > 0 {
		opts := wbxml.E(wbxml.PageItemOperations, "Options")
		rng := itoa64(rangeStart) + "-"
		if rangeEnd > 0 {
			rng += itoa64(rangeEnd)
		}
		opts.Children = append(opts.Children, wbxml.E(wbxml.PageItemOperations, "Range", wbxml.Text(rng)))
		fetch.Children = append(fetch.Children, opts)
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations", fetch),
	}
	resp, err := c.post(ctx, "ItemOperations", doc)
	if err != nil {
		return nil, err
	}
	if err := checkItemOperationsStatus(resp, "ItemOperations/FetchAttachment"); err != nil {
		return nil, err
	}
	r := resp.Root.Find("Response")
	if r == nil {
		return nil, errors.New("eas: FetchAttachment: missing Response")
	}
	f := r.Find("Fetch")
	if f == nil {
		return nil, errors.New("eas: FetchAttachment: missing Fetch")
	}
	out := &FetchAttachmentResult{}
	if rng := f.Find("Range"); rng != nil {
		out.Range = rng.TextContent()
	}
	if props := f.Find("Properties"); props != nil {
		if ct := props.Find("ContentType"); ct != nil {
			out.ContentType = ct.TextContent()
		}
		if data := props.Find("Data"); data != nil {
			if op := firstOpaque(data); op != nil {
				out.Data = op
			} else {
				out.Data = []byte(data.TextContent())
			}
		}
	}
	if len(out.Data) == 0 {
		return nil, errors.New("eas: FetchAttachment: no data in response")
	}
	return out, nil
}

// boolNumString returns "1" for true, "0" for false (matching EAS
// element conventions).
func boolNumString(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// checkItemOperationsStatus inspects the top-level Status of an
// ItemOperations response and returns an error if not OK.
func checkItemOperationsStatus(resp *wbxml.Document, cmd string) error {
	if resp == nil || resp.Root == nil {
		return errors.New("eas: " + cmd + ": empty response")
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return &StatusError{Command: cmd, Code: st}
	}
	return nil
}
