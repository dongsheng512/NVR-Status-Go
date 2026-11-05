// Command nvrtool holds the repository's build-time helpers. Nothing here is
// linked into the shipped app; it exists so the toolchain needs only Go.
//
//	go run ./cmd/nvrtool bundle-ffmpeg [--check] [--require-ffmpeg]
//	go run ./cmd/nvrtool check-secrets [--all]
package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "bundle-ffmpeg":
		err = runBundle(os.Args[2:])
	case "check-secrets":
		err = runSecrets(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "nvrtool: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		// runSecrets prints its own explanation and only needs a non-zero exit.
		if !errors.Is(err, errBlocked) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: nvrtool <command> [flags]

  bundle-ffmpeg   bundle ffmpeg + its dylib closure into resources/darwin/bin
  check-secrets   block commits containing real device names / IPs
`)
}
