package main

import (
	"sort"
	"strings"
	"unicode"

	"github.com/hstern/go-activesync/eas"
)

func isMailFolderType(folderType eas.FolderType) bool {
	switch folderType {
	case eas.FolderTypeInbox,
		eas.FolderTypeDrafts,
		eas.FolderTypeDeletedItems,
		eas.FolderTypeSentItems,
		eas.FolderTypeOutbox,
		eas.FolderTypeUserMail:
		return true
	default:
		return false
	}
}

func systemIMAPFolderName(folderType eas.FolderType) string {
	switch folderType {
	case eas.FolderTypeInbox:
		return "INBOX"
	case eas.FolderTypeSentItems:
		return "Sent"
	case eas.FolderTypeDrafts:
		return "Drafts"
	case eas.FolderTypeDeletedItems:
		return "Trash"
	case eas.FolderTypeOutbox:
		return "Outbox"
	default:
		return ""
	}
}

// imapFolderNames returns one stable, selectable IMAP name for each mail folder.
// Custom folders preserve the EAS parent hierarchy. Duplicate display names get a
// short deterministic suffix so LIST and SELECT never point at different folders.
func imapFolderNames(folders []eas.Folder) map[string]string {
	byID := make(map[string]eas.Folder, len(folders))
	for _, folder := range folders {
		byID[folder.ServerID] = folder
	}

	segments := make(map[string]string, len(folders))
	duplicateSegments := make(map[string][]string)
	for _, folder := range folders {
		if !isMailFolderType(folder.Type) {
			continue
		}
		segment := systemIMAPFolderName(folder.Type)
		if segment == "" {
			segment = cleanMailboxComponent(folder.DisplayName)
		}
		segments[folder.ServerID] = segment
		key := strings.ToLower(folder.ParentID + "\x00" + segment)
		duplicateSegments[key] = append(duplicateSegments[key], folder.ServerID)
	}
	for _, ids := range duplicateSegments {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		hasSystem := false
		for _, id := range ids {
			if systemIMAPFolderName(byID[id].Type) != "" {
				hasSystem = true
				break
			}
		}
		for i, id := range ids {
			if hasSystem && systemIMAPFolderName(byID[id].Type) != "" {
				continue
			}
			if !hasSystem && i == 0 {
				continue
			}
			segments[id] += " [" + cachePathComponent("folder-name", id)[:8] + "]"
		}
	}

	names := make(map[string]string, len(folders))
	visiting := make(map[string]bool)
	var resolve func(string) string
	resolve = func(id string) string {
		if name := names[id]; name != "" {
			return name
		}
		folder, ok := byID[id]
		if !ok || !isMailFolderType(folder.Type) {
			return ""
		}
		segment := segments[id]
		if systemIMAPFolderName(folder.Type) != "" {
			names[id] = segment
			return segment
		}

		if visiting[id] {
			names[id] = segment
			return segment
		}
		visiting[id] = true
		parent := resolve(folder.ParentID)
		delete(visiting, id)
		if parent != "" {
			names[id] = parent + "/" + segment
		} else {
			names[id] = segment
		}
		return names[id]
	}

	for id, folder := range byID {
		if isMailFolderType(folder.Type) {
			resolve(id)
		}
	}

	return names
}

func cleanMailboxComponent(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/':
			return '∕'
		case unicode.IsControl(r):
			return -1
		default:
			return r
		}
	}, name)
	if name == "" {
		return "Folder"
	}
	return name
}
