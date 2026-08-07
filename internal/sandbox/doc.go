// Package sandbox builds fail-closed OS sandbox launch specifications for
// short-lived worker processes.
//
// It deliberately does not execute commands. The generated boundary limits
// filesystem writes and mounts; network access remains open so normal
// development tools and package managers work inside the worker.
package sandbox
