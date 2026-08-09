package handlers

import "testing"

func TestNormalizeServerConnection(t *testing.T) {
	tests := []struct {
		name         string
		serverName   string
		host         string
		username     string
		port         int
		wantName     string
		wantHost     string
		wantUsername string
		wantPort     int
		wantErr      bool
	}{
		{
			name:         "trims tabs and CRLF",
			serverName:   "\t Guangzhou node \r\n",
			host:         "\t162.35.171.46\r\n",
			username:     "\troot\r\n",
			port:         0,
			wantName:     "Guangzhou node",
			wantHost:     "162.35.171.46",
			wantUsername: "root",
			wantPort:     22,
		},
		{
			name:         "normalizes bracketed IPv6",
			serverName:   "IPv6 node",
			host:         "[2001:db8::1]",
			username:     "root",
			port:         2222,
			wantName:     "IPv6 node",
			wantHost:     "2001:db8::1",
			wantUsername: "root",
			wantPort:     2222,
		},
		{
			name:         "keeps bare IPv6",
			serverName:   "IPv6 node",
			host:         "2001:db8::2",
			username:     "",
			port:         65535,
			wantName:     "IPv6 node",
			wantHost:     "2001:db8::2",
			wantUsername: "root",
			wantPort:     65535,
		},
		{name: "rejects empty name", serverName: "\t\r\n", host: "example.com", username: "root", port: 22, wantErr: true},
		{name: "rejects empty host", serverName: "node", host: "\t\r\n", username: "root", port: 22, wantErr: true},
		{name: "rejects internal host whitespace", serverName: "node", host: "example .com", username: "root", port: 22, wantErr: true},
		{name: "rejects internal host control character", serverName: "node", host: "example\t.com", username: "root", port: 22, wantErr: true},
		{name: "rejects zero width host character", serverName: "node", host: "example\u200b.com", username: "root", port: 22, wantErr: true},
		{name: "rejects embedded port", serverName: "node", host: "example.com:22", username: "root", port: 22, wantErr: true},
		{name: "rejects unmatched bracket", serverName: "node", host: "[2001:db8::1", username: "root", port: 22, wantErr: true},
		{name: "rejects bracketed hostname", serverName: "node", host: "[example.com]", username: "root", port: 22, wantErr: true},
		{name: "rejects internal username whitespace", serverName: "node", host: "example.com", username: "root user", port: 22, wantErr: true},
		{name: "rejects negative port", serverName: "node", host: "example.com", username: "root", port: -1, wantErr: true},
		{name: "rejects port above maximum", serverName: "node", host: "example.com", username: "root", port: 65536, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotName, gotHost, gotUsername, gotPort, err := normalizeServerConnection(
				test.serverName,
				test.host,
				test.username,
				test.port,
			)
			if test.wantErr {
				if err == nil {
					t.Fatalf("normalizeServerConnection() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeServerConnection() unexpected error: %v", err)
			}
			if gotName != test.wantName || gotHost != test.wantHost || gotUsername != test.wantUsername || gotPort != test.wantPort {
				t.Fatalf(
					"normalizeServerConnection() = (%q, %q, %q, %d), want (%q, %q, %q, %d)",
					gotName, gotHost, gotUsername, gotPort,
					test.wantName, test.wantHost, test.wantUsername, test.wantPort,
				)
			}
		})
	}
}

func TestNormalizeCredentialFields(t *testing.T) {
	name, username, credentialType, err := normalizeCredentialFields("  shared root  ", "\troot\r\n", " LINUX ")
	if err != nil {
		t.Fatalf("normalizeCredentialFields() unexpected error: %v", err)
	}
	if name != "shared root" || username != "root" || credentialType != "linux" {
		t.Fatalf("normalizeCredentialFields() = (%q, %q, %q)", name, username, credentialType)
	}

	for _, test := range []struct {
		name           string
		credentialName string
		username       string
		credentialType string
	}{
		{name: "blank name", credentialName: " \t ", username: "root", credentialType: "linux"},
		{name: "blank username", credentialName: "shared", username: " \r\n", credentialType: "linux"},
		{name: "username internal whitespace", credentialName: "shared", username: "root user", credentialType: "linux"},
		{name: "invalid type", credentialName: "shared", username: "root", credentialType: "other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := normalizeCredentialFields(test.credentialName, test.username, test.credentialType); err == nil {
				t.Fatal("normalizeCredentialFields() error = nil, want an error")
			}
		})
	}
}
