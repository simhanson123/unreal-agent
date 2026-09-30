//go:build windows

package openaicodex

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	accessAllowedCallbackACEType = 0x9
	accessDeniedCallbackACEType  = 0xA

	// Rights that let another account read, replace, or re-permission the credential file.
	sensitiveFileRights = windows.ACCESS_MASK(0x0001 | // FILE_READ_DATA
		0x0002 | // FILE_WRITE_DATA
		0x0004 | // FILE_APPEND_DATA
		windows.WRITE_DAC | windows.WRITE_OWNER |
		windows.GENERIC_READ | windows.GENERIC_WRITE | windows.GENERIC_ALL)
)

var errPublicAuthFile = errors.New("codex auth file must be a regular file private to the current user; remove access for other accounts (inspect with icacls)")

// Local group the Codex Windows sandbox creates for its sandbox accounts.
const codexSandboxGroup = "CodexSandboxUsers"

// checkPrivateAuthFile is the Windows counterpart of the POSIX 0600 check: only the current
// user, LocalSystem, Administrators, and principals the Codex Windows sandbox manages for this
// Codex home may hold sensitive rights on the file.
func checkPrivateAuthFile(file *os.File, _ os.FileInfo) error {
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect Codex auth file permissions: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("inspect Codex auth file permissions: %w", err)
	}
	if dacl == nil {
		// A null DACL grants everyone full access.
		return errPublicAuthFile
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("inspect Codex auth file owner: %w", err)
	}
	trusted, err := trustedAuthFileSIDs()
	if err != nil {
		return err
	}
	trusted = append(trusted, codexSandboxSIDs(filepath.Dir(file.Name()))...)
	isTrusted := func(sid *windows.SID) bool {
		for _, candidate := range trusted {
			if sid.Equals(candidate) {
				return true
			}
		}
		return false
	}
	ownerRights, err := windows.CreateWellKnownSid(windows.WinCreatorOwnerRightsSid)
	if err != nil {
		return fmt.Errorf("resolve owner rights SID: %w", err)
	}
	var others []string
	for index := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("inspect Codex auth file permissions: %w", err)
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE, accessDeniedCallbackACEType:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE, accessAllowedCallbackACEType:
		default:
			// Object ACEs use a different layout and do not belong on a plain file.
			return errPublicAuthFile
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Mask&sensitiveFileRights == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.Equals(ownerRights) {
			sid = owner
		}
		if !isTrusted(sid) {
			others = append(others, accountName(sid))
		}
	}
	if len(others) != 0 {
		return fmt.Errorf("%w (accounts with access: %s)", errPublicAuthFile, strings.Join(others, ", "))
	}
	return nil
}

func accountName(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	if domain == "" {
		return account
	}
	return domain + `\` + account
}

func trustedAuthFileSIDs() ([]*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("resolve current user: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("resolve LocalSystem SID: %w", err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, fmt.Errorf("resolve Administrators SID: %w", err)
	}
	return []*windows.SID{user.User.Sid, system, administrators}, nil
}

// codexSandboxSIDs returns the principals Codex grants on its own files: the sandbox users group
// and the capability SIDs recorded in codexHome/cap_sid. Capability SIDs are trusted only when
// they map to no account, since they then take effect solely inside this user's restricted tokens.
func codexSandboxSIDs(codexHome string) []*windows.SID {
	var sids []*windows.SID
	if group, _, kind, err := windows.LookupSID("", codexSandboxGroup); err == nil && kind == windows.SidTypeAlias {
		sids = append(sids, group)
	}
	encoded, err := os.ReadFile(filepath.Join(codexHome, "cap_sid"))
	if err != nil || len(encoded) > 4<<20 {
		return sids
	}
	var capabilities any
	if json.Unmarshal(encoded, &capabilities) != nil {
		return sids
	}
	var collect func(value any)
	collect = func(value any) {
		switch value := value.(type) {
		case string:
			sid, err := windows.StringToSid(value)
			if err != nil {
				return
			}
			if _, _, _, err := sid.LookupAccount(""); err == nil {
				return
			}
			sids = append(sids, sid)
		case map[string]any:
			for _, nested := range value {
				collect(nested)
			}
		case []any:
			for _, nested := range value {
				collect(nested)
			}
		}
	}
	collect(capabilities)
	return sids
}
