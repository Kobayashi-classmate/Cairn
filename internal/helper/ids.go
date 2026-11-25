// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"fmt"
	"strconv"
)

// parseID reads a user or group ID as the passwd and group databases write
// it. The kernel's IDs are 32 bits wide, and that is the width the helper
// hands them on in (credentials of a command, chown): a larger number must
// be refused where it is read, not wrap where it is used. 4294967296 passes
// every "is this root?" check as a number and arrives as 0.
//
// IDs up to 2147483647 are accepted. The upper half of the range holds no
// account a panel works with (the last ID is "leave as it is" to chown,
// the one before it the overflow ID of old systems), and keeping to 31 bits
// lets the same number be a signed or an unsigned 32-bit value.
func parseID(s string) (int, error) {
	n, err := strconv.ParseUint(s, 10, 31)
	if err != nil {
		return 0, fmt.Errorf("user or group ID %q: %w", s, err)
	}
	return int(n), nil
}
