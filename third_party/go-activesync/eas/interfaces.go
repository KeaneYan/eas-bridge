// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import "context"

// Client is the full EAS client surface. It composes one interface per
// feature area so callers that only touch a slice of the protocol can
// depend on the narrower view (e.g. an inbox-summarising tool needs
// only EmailClient + FolderClient).
//
// NewClient returns a value satisfying Client; the concrete type is
// unexported. For unit tests, the github.com/hstern/go-activesync/eas/easmock
// package provides hand-written test doubles for Client and each
// sub-interface.
type Client interface {
	EmailClient
	CalendarClient
	ContactsClient
	TasksClient
	NotesClient
	FolderClient
	SettingsClient
	SearchClient
	ProvisionClient
	PingClient

	// LastPolicy returns the most recently parsed Policy from a
	// Provision exchange on this client, or nil if no policy has been
	// received yet.
	LastPolicy() *Policy
}

// EmailClient covers the email read/write/search/send surface.
type EmailClient interface {
	SyncEmail(ctx context.Context, folderID string, opts EmailSyncOptions) (*EmailSyncResult, error)
	ApplyEmailChanges(ctx context.Context, folderID string, changes []EmailChange) ([]EmailChangeResult, error)
	FetchEmail(ctx context.Context, folderID, serverID string, opts FetchEmailOptions) (*EmailItem, error)
	SendMail(ctx context.Context, opts SendMailOptions) error
	SmartReply(ctx context.Context, opts ReplyForwardOptions) error
	SmartForward(ctx context.Context, opts ReplyForwardOptions) error
	SearchEmail(ctx context.Context, query string, opts EmailSearchOptions) (*EmailSearchResult, error)
	SearchEmailQuery(ctx context.Context, q Query, opts EmailSearchOptions) (*EmailSearchResult, error)
	FindEmail(ctx context.Context, query string, opts FindOptions) (*FindResult, error)
}

// CalendarClient covers calendar sync, event CRUD, and meeting-response.
type CalendarClient interface {
	SyncCalendar(ctx context.Context, folderID string, opts CalendarSyncOptions) (*CalendarSyncResult, error)
	CreateEvent(ctx context.Context, folderID string, draft EventDraft) (string, error)
	UpdateEvent(ctx context.Context, folderID, serverID string, draft EventDraft) error
	DeleteEvent(ctx context.Context, folderID, serverID string) error
	RespondInvite(ctx context.Context, folderID, serverID string, choice MeetingResponseChoice) (*MeetingResponseResult, error)
}

// ContactsClient covers contact-folder sync and CRUD.
type ContactsClient interface {
	SyncContacts(ctx context.Context, folderID string) (*ContactsSyncResult, error)
	CreateContact(ctx context.Context, folderID string, draft ContactDraft) (string, error)
	UpdateContact(ctx context.Context, folderID, serverID string, draft ContactDraft) error
	DeleteContact(ctx context.Context, folderID, serverID string) error
}

// TasksClient covers task-folder sync, CRUD, and the convenience
// CompleteTask helper.
type TasksClient interface {
	SyncTasks(ctx context.Context, folderID string) (*TasksSyncResult, error)
	CreateTask(ctx context.Context, folderID string, draft TaskDraft) (string, error)
	UpdateTask(ctx context.Context, folderID, serverID string, draft TaskDraft) error
	CompleteTask(ctx context.Context, folderID, serverID string) error
	DeleteTask(ctx context.Context, folderID, serverID string) error
}

// NotesClient covers notes-folder sync and CRUD.
type NotesClient interface {
	SyncNotes(ctx context.Context, folderID string) (*NotesSyncResult, error)
	CreateNote(ctx context.Context, folderID string, draft NoteDraft) (string, error)
	UpdateNote(ctx context.Context, folderID, serverID string, draft NoteDraft) error
	DeleteNote(ctx context.Context, folderID, serverID string) error
}

// FolderClient covers folder-hierarchy sync, folder CRUD, item-estimate,
// move-items, attachment / document-library fetch, and folder-empty.
type FolderClient interface {
	FolderSync(ctx context.Context) (*FolderSyncResult, error)
	FolderCreate(ctx context.Context, parentID, displayName string, folderType FolderType) (*FolderCreateResult, error)
	FolderUpdate(ctx context.Context, serverID, newParentID, newDisplayName string) error
	FolderDelete(ctx context.Context, serverID string) error
	GetItemEstimate(ctx context.Context, folderIDs []string) ([]ItemEstimate, error)
	MoveItems(ctx context.Context, srcFolder, dstFolder string, ids []string) ([]MoveItemResult, error)
	MoveViaItemOperations(ctx context.Context, srcFolder, srcID, dstFolder string, moveAlways bool) (string, error)
	EmptyFolderContents(ctx context.Context, folderID string, deleteSubfolders bool) error
	FetchAttachment(ctx context.Context, fileReference string, rangeStart, rangeEnd int64) (*FetchAttachmentResult, error)
	FetchDocumentLibrary(ctx context.Context, linkID string, rangeStart, rangeEnd int64) ([]byte, error)
}

// SettingsClient covers Out-of-Office, device password, user info, and
// rights-management template enumeration.
type SettingsClient interface {
	GetOof(ctx context.Context) (*OofConfig, error)
	SetOof(ctx context.Context, cfg OofConfig) error
	SetDevicePassword(ctx context.Context, newPassword string) error
	GetUserInformation(ctx context.Context) (*UserInformation, error)
	SettingsDeviceInformation(ctx context.Context, info DeviceInformation) error
	GetRightsManagementTemplates(ctx context.Context) ([]RightsTemplate, error)
}

// SearchClient covers GAL search, recipient resolution, and S/MIME
// certificate validation.
type SearchClient interface {
	GALSearch(ctx context.Context, query string, limit int) (*GALSearchResult, error)
	ResolveRecipients(ctx context.Context, recipients []string, opts ResolveOptions) ([]ResolveResponse, error)
	ValidateCert(ctx context.Context, certs, chain [][]byte, checkCRL bool) ([]CertValidation, error)
}

// ProvisionClient covers the policy handshake, remote-wipe ack, and
// pre-flight version negotiation / OPTIONS probing.
type ProvisionClient interface {
	Provision(ctx context.Context) error
	AcknowledgeRemoteWipe(ctx context.Context, status int) error
	NegotiateVersion(ctx context.Context) (string, error)
	Options(ctx context.Context) (*OptionsResult, error)
}

// PingClient covers long-poll change notification.
type PingClient interface {
	Ping(ctx context.Context, heartbeatSeconds int, folders []PingFolder) (*PingResult, error)
}
