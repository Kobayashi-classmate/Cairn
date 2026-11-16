// SPDX-License-Identifier: AGPL-3.0-only

// Command agekey prints a fresh age key pair for the backups test: the
// recipient on the first line, the identity on the second. Nothing is
// kept; every test run uses a new pair.
package main

import (
	"fmt"
	"os"

	"filippo.io/age"
)

func main() {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(id.Recipient().String())
	fmt.Println(id.String())
}
