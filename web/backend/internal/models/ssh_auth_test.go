package models

import "testing"

func TestSSHAuthenticationUpdateSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, oldPassword, oldKey, password, key, platform string
		update                                             SSHAuthUpdate
		wantPassword, wantKey                              string
		invalid                                            bool
	}{
		{name: "retain password", oldPassword: "old", platform: "linux", wantPassword: "old"},
		{name: "retain key", oldKey: "old", platform: "linux", update: SSHAuthUpdate{Method: "key"}, wantKey: "old"},
		{name: "switch password to key", oldPassword: "old", key: "new", platform: "linux", update: SSHAuthUpdate{Method: "key"}, wantKey: "new"},
		{name: "switch key to password", oldKey: "old", password: "new", platform: "linux", update: SSHAuthUpdate{Method: "password"}, wantPassword: "new"},
		{name: "explicit clear keeps remaining key", oldPassword: "old", oldKey: "key", platform: "linux", update: SSHAuthUpdate{ClearPassword: true}, wantKey: "key"},
		{name: "clear selected password rejected", oldPassword: "old", platform: "linux", update: SSHAuthUpdate{ClearPassword: true}, invalid: true},
		{name: "clear and set rejected", password: "new", platform: "linux", update: SSHAuthUpdate{ClearPassword: true}, invalid: true},
		{name: "windows cannot discard its only key", oldKey: "old", platform: "windows", invalid: true},
		{name: "windows switch with password", oldKey: "old", password: "new", platform: "windows", wantPassword: "new"},
		{name: "windows forbids key selection", oldPassword: "old", oldKey: "old", platform: "windows", update: SSHAuthUpdate{Method: "key"}, invalid: true},
		{name: "missing selected method fails even with other material", oldPassword: "old", platform: "linux", update: SSHAuthUpdate{Method: "key"}, invalid: true},
		{name: "unknown method fails", oldPassword: "old", platform: "linux", update: SSHAuthUpdate{Method: "auto"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			password, key, err := mergeSSHAuthentication(tc.oldPassword, tc.oldKey, tc.password, tc.key, tc.platform, tc.update)
			if (err != nil) != tc.invalid {
				t.Fatalf("err=%v invalid=%v", err, tc.invalid)
			}
			if !tc.invalid && (password != tc.wantPassword || key != tc.wantKey) {
				t.Fatal("unexpected effective authentication")
			}
		})
	}
}
