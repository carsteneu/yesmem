package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

func runAgentTTY() {
	fs := flag.NewFlagSet("agent-tty", flag.ExitOnError)
	sock := fs.String("sock", "", "Unix socket path to connect to")
	fs.Parse(os.Args[2:])

	if *sock == "" {
		fmt.Fprintln(os.Stderr, "Error: --sock is required")
		os.Exit(1)
	}

	conn, err := net.Dial("unix", *sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect %s: %v\n", *sock, err)
		os.Exit(1)
	}
	defer conn.Close()

	// Send initial terminal size (4 bytes: rows u16 + cols u16)
	sendWinsize(conn)

	// Set terminal to raw mode so keystrokes pass through immediately
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err == nil {
		defer term.Restore(fd, oldState)
	}

	// Handle SIGWINCH — forward terminal resize to bridge
	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	go func() {
		for range winchCh {
			sendWinsize(conn)
		}
	}()

	// Handle Ctrl-C gracefully — restore terminal before exit
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		if oldState != nil {
			term.Restore(fd, oldState)
		}
		os.Exit(0)
	}()

	// Bidirectional bridge: terminal ↔ socket ↔ PTY ↔ claude
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(conn, os.Stdin)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(os.Stdout, conn)
		done <- struct{}{}
	}()
	<-done
}

// sendWinsize sends the current terminal size over the connection.
// Protocol: 1 byte marker (0x01) + 2 bytes rows + 2 bytes cols (big-endian).
func sendWinsize(conn net.Conn) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return
	}
	buf := make([]byte, 5)
	buf[0] = 0x01 // winsize marker
	binary.BigEndian.PutUint16(buf[1:3], uint16(h))
	binary.BigEndian.PutUint16(buf[3:5], uint16(w))
	conn.Write(buf)
}
