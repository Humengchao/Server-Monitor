package models

import (
	"errors"
	"strings"
)

var ErrSSHAuthentication = errors.New("invalid SSH authentication configuration")

// SSHAuthUpdate distinguishes retaining an existing secret from deliberately
// clearing it. An explicit method selects one method and removes the other.
type SSHAuthUpdate struct {
	Method        string
	ClearPassword bool
	ClearKey      bool
}

func EffectiveSSHAuthMethod(password, key string) string {
	if password != "" {
		return "password"
	}
	if strings.TrimSpace(key) != "" {
		return "key"
	}
	return ""
}

// Values may be encrypted: only presence is inspected here. Merge and validate
// under the row lock, before replacing any persisted authentication material.
func mergeSSHAuthentication(oldPassword, oldKey, password, key, platform string, update SSHAuthUpdate) (string, string, error) {
	if update.Method != "" && update.Method != "password" && update.Method != "key" ||
		update.ClearPassword && password != "" || update.ClearKey && key != "" {
		return "", "", ErrSSHAuthentication
	}
	if password == "" && !update.ClearPassword {
		password = oldPassword
	}
	if key == "" && !update.ClearKey {
		key = oldKey
	}
	if platform == "windows" {
		if update.Method == "key" {
			return "", "", ErrSSHAuthentication
		}
		key = ""
	}
	switch update.Method {
	case "password":
		key = ""
		if password == "" {
			return "", "", ErrSSHAuthentication
		}
	case "key":
		password = ""
		if strings.TrimSpace(key) == "" {
			return "", "", ErrSSHAuthentication
		}
	}
	if EffectiveSSHAuthMethod(password, key) == "" {
		return "", "", ErrSSHAuthentication
	}
	return password, key, nil
}
