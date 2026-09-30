//go:build windows

package openaicodex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// makeAuthFilePrivate replaces inherited permissions, which may include other accounts
// (for example sandbox groups on the temporary directory), with a protected owner-only DACL.
func makeAuthFilePrivate(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}
	for _, sidType := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		sid, err := windows.CreateWellKnownSid(sidType)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

func makeAuthFilePublic(t *testing.T, path string) {
	t.Helper()
	setAuthFileACE(t, path, windows.WinWorldSid, windows.GRANT_ACCESS, windows.GENERIC_READ)
}

// setAuthFileACE merges one explicit ACE for a well-known SID into the file's DACL.
func setAuthFileACE(t *testing.T, path string, sidType windows.WELL_KNOWN_SID_TYPE, mode windows.ACCESS_MODE, rights windows.ACCESS_MASK) {
	t.Helper()
	sid, err := windows.CreateWellKnownSid(sidType)
	if err != nil {
		t.Fatal(err)
	}
	setAuthFileSIDACE(t, path, sid, mode, rights)
}

func setAuthFileSIDACE(t *testing.T, path string, sid *windows.SID, mode windows.ACCESS_MODE, rights windows.ACCESS_MASK) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	updated, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: rights,
		AccessMode:        mode,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, current)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, updated, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsAuthFileAccessControl(t *testing.T) {
	for _, test := range []struct {
		name   string
		modify func(t *testing.T, path string)
		public bool
	}{
		{name: "owner-only permissions", modify: func(*testing.T, string) {}},
		{name: "everyone can read", modify: makeAuthFilePublic, public: true},
		{name: "users can read", modify: func(t *testing.T, path string) {
			setAuthFileACE(t, path, windows.WinBuiltinUsersSid, windows.GRANT_ACCESS, 0x0001)
		}, public: true},
		{name: "authenticated users can change permissions", modify: func(t *testing.T, path string) {
			setAuthFileACE(t, path, windows.WinAuthenticatedUserSid, windows.GRANT_ACCESS, windows.WRITE_DAC)
		}, public: true},
		{name: "everyone may only read attributes", modify: func(t *testing.T, path string) {
			setAuthFileACE(t, path, windows.WinWorldSid, windows.GRANT_ACCESS, windows.FILE_READ_ATTRIBUTES)
		}},
		{name: "guests are denied", modify: func(t *testing.T, path string) {
			setAuthFileACE(t, path, windows.WinBuiltinGuestsSid, windows.DENY_ACCESS, windows.GENERIC_ALL)
		}},
		{name: "null DACL", modify: func(t *testing.T, path string) {
			if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
		}, public: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			writeTestAuth(t, path, "opaque", "account")
			test.modify(t, path)
			got, err := (Config{AuthFile: path}).credentials()
			if test.public {
				if err == nil || !strings.Contains(err.Error(), "private to the current user") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || got.accessToken != "opaque" {
				t.Fatalf("credentials error = %v", err)
			}
		})
	}
}

func TestWindowsAuthFileTrustsCodexCapabilitySIDs(t *testing.T) {
	// An unmapped SID like the capability SIDs Codex records for sandboxed workspaces.
	capability, err := windows.StringToSid("S-1-5-21-1-2-3-4")
	if err != nil {
		t.Fatal(err)
	}
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, capSID string
		grant        *windows.SID
		public       bool
	}{
		{name: "unlisted capability", grant: capability, public: true},
		{name: "listed capability", capSID: `{"workspace":"S-1-5-21-9-9-9-9","workspace_by_cwd":{"c:/users/example":"S-1-5-21-1-2-3-4"}}`, grant: capability},
		{name: "listed account is still rejected", capSID: `{"workspace":"S-1-5-32-545"}`, grant: users, public: true},
		{name: "invalid cap_sid", capSID: `not json`, grant: capability, public: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "auth.json")
			writeTestAuth(t, path, "opaque", "account")
			setAuthFileSIDACE(t, path, test.grant, windows.GRANT_ACCESS, windows.GENERIC_READ|windows.GENERIC_WRITE)
			if test.capSID != "" {
				if err := os.WriteFile(filepath.Join(home, "cap_sid"), []byte(test.capSID), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := (Config{AuthFile: path}).credentials()
			if test.public != (err != nil) {
				t.Fatalf("error = %v", err)
			}
			if test.public && !strings.Contains(err.Error(), "accounts with access") {
				t.Fatalf("error does not name the accounts: %v", err)
			}
		})
	}
}
