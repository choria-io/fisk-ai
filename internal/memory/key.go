//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package memory

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// keyPattern is the intersection of characters legal in a NATS KV key and safe
// in a filename: letters, digits, dot, underscore, equals and dash. The forward
// slash a NATS KV key also allows is deliberately excluded so a key maps one to
// one to a flat filename with no path separator to escape or traverse.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._=-]+$`)

// ValidateKey reports whether key is a legal memory key. The rules mirror what a
// NATS KV key allows (minus the slash) so a value written by the file backend
// migrates to a KV backend unchanged, and they are strict enough that a key can
// never traverse out of the file backend's directory: no slash, no leading or
// trailing dot, and no empty token (".."), on top of the charset.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("memory key must not be empty")
	}
	if utf8.RuneCountInString(key) > maxKeyRunes {
		return fmt.Errorf("memory key is too long: at most %d characters", maxKeyRunes)
	}
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("memory key %q is invalid: use only letters, digits and '.', '_', '=' or '-' (no slashes or spaces)", key)
	}
	if strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".") {
		return fmt.Errorf("memory key %q is invalid: it must not start or end with '.'", key)
	}
	if strings.Contains(key, "..") {
		return fmt.Errorf("memory key %q is invalid: it must not contain '..'", key)
	}

	return nil
}
