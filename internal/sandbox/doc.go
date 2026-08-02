// Package sandbox builds fail-closed OS sandbox launch specifications for
// short-lived worker processes.
//
// It deliberately does not execute commands or proxy network traffic. The
// caller must use CommandSpec with an exec.Cmd and, when network access is
// configured, run a proxy that enforces Policy.Network.AllowedHosts. Linux
// workers additionally use the CommandSpec relay fields to bridge their
// isolated loopback namespace to that proxy through a Unix socket.
package sandbox
