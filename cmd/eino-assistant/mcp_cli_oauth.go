package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/mcpoauth"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

const defaultMCPOAuthLoginTimeout = 5 * time.Minute

type mcpOAuthCredentialStore interface {
	Save(serverName, endpoint string, token *oauth2.Token) error
	Delete(serverName string) error
}

type mcpOAuthCommandDeps struct {
	login       func(context.Context, string, mcpoauth.LoginOptions) (*oauth2.Token, error)
	newStore    func() mcpOAuthCredentialStore
	openBrowser func(string) error
}

func defaultMCPOAuthCommandDeps() mcpOAuthCommandDeps {
	return mcpOAuthCommandDeps{
		login: mcpoauth.Login,
		newStore: func() mcpOAuthCredentialStore {
			return mcpoauth.NewSystemStore()
		},
		openBrowser: openMCPAuthorizationURL,
	}
}

func newMCPLoginCommand(opts *rootOptions) *cobra.Command {
	return newMCPLoginCommandWithDeps(opts, defaultMCPOAuthCommandDeps())
}

func newMCPLoginCommandWithDeps(opts *rootOptions, deps mcpOAuthCommandDeps) *cobra.Command {
	var timeout time.Duration
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "login <name>",
		Short: "Authenticate one remote MCP server with OAuth",
		Long:  "Authenticate one Streamable HTTP MCP server with OAuth. Eino uses metadata discovery, dynamic client registration, PKCE, and a loopback callback; it stores the resulting credential only in the system keyring.",
		Args:  validMCPServerNameArgument,
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 {
				return errors.New("MCP OAuth login timeout must be positive")
			}
			return loginMCPServer(cmd.Context(), opts.configPath, args[0], timeout, noBrowser, cmd.OutOrStdout(), cmd.ErrOrStderr(), deps)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", defaultMCPOAuthLoginTimeout, "maximum time to wait for OAuth browser authorization")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the authorization URL without trying to open a browser")
	return cmd
}

func newMCPLogoutCommand(opts *rootOptions) *cobra.Command {
	return newMCPLogoutCommandWithDeps(opts, defaultMCPOAuthCommandDeps())
}

func newMCPLogoutCommandWithDeps(opts *rootOptions, deps mcpOAuthCommandDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "logout <name>",
		Short: "Clear one remote MCP OAuth credential",
		Long:  "Clear one locally stored Streamable HTTP MCP OAuth credential and disable OAuth for future runtimes. It does not revoke a provider-side token.",
		Args:  validMCPServerNameArgument,
		RunE: func(cmd *cobra.Command, args []string) error {
			return logoutMCPServer(opts.configPath, args[0], cmd.OutOrStdout(), deps)
		},
	}
}

func validMCPServerNameArgument(cmd *cobra.Command, args []string) error {
	if err := cobra.ExactArgs(1)(cmd, args); err != nil {
		return err
	}
	if strings.TrimSpace(args[0]) == "" {
		return errors.New("MCP server name is required")
	}
	return nil
}

func loginMCPServer(ctx context.Context, configPath, name string, timeout time.Duration, noBrowser bool, stdout, stderr io.Writer, deps mcpOAuthCommandDeps) error {
	server, err := configuredMCPServerForOAuth(configPath, name)
	if err != nil {
		return err
	}
	if deps.login == nil || deps.newStore == nil {
		return errors.New("MCP OAuth login is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	flowCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	token, err := deps.login(flowCtx, server.URL, mcpoauth.LoginOptions{
		AuthorizationURL: func(callbackCtx context.Context, authorizationURL string) error {
			if _, writeErr := fmt.Fprintln(stdout, "Open the following URL to authorize MCP access:"); writeErr != nil {
				return writeErr
			}
			if _, writeErr := fmt.Fprintln(stdout, authorizationURL); writeErr != nil {
				return writeErr
			}
			if noBrowser || deps.openBrowser == nil {
				return nil
			}
			if openErr := deps.openBrowser(authorizationURL); openErr != nil {
				_, _ = fmt.Fprintln(stderr, "Could not open a browser automatically; open the URL above to continue.")
			}
			return callbackCtx.Err()
		},
	})
	if err != nil {
		return fmt.Errorf("log in to MCP server %q: %w", server.Name, err)
	}
	store := deps.newStore()
	if store == nil {
		return errors.New("MCP OAuth keyring is unavailable")
	}
	if err := store.Save(server.Name, server.URL, token); err != nil {
		return err
	}
	if current, currentErr := configuredMCPServerForOAuth(configPath, server.Name); currentErr != nil || current.URL != server.URL {
		cleanupErr := store.Delete(server.Name)
		if cleanupErr != nil && !errors.Is(cleanupErr, mcpoauth.ErrNotFound) {
			return fmt.Errorf("MCP server configuration changed during login; also could not remove the new credential: %w", cleanupErr)
		}
		if currentErr != nil {
			return fmt.Errorf("MCP server configuration changed during login: %w", currentErr)
		}
		return errors.New("MCP server endpoint changed during login; no credential was kept")
	}
	if err := config.SetMCPOAuthEnabled(configPath, server.Name, true); err != nil {
		cleanupErr := store.Delete(server.Name)
		if cleanupErr != nil && !errors.Is(cleanupErr, mcpoauth.ErrNotFound) {
			return fmt.Errorf("enable OAuth for MCP server %q: %w; also could not remove the new credential: %v", server.Name, err, cleanupErr)
		}
		return fmt.Errorf("enable OAuth for MCP server %q: %w", server.Name, err)
	}
	_, err = fmt.Fprintf(stdout, "Logged in to MCP server %q.\n", server.Name)
	return err
}

func logoutMCPServer(configPath, name string, stdout io.Writer, deps mcpOAuthCommandDeps) error {
	server, err := configuredMCPServerForOAuth(configPath, name)
	if err != nil {
		return err
	}
	if deps.newStore == nil {
		return errors.New("MCP OAuth logout is unavailable")
	}
	store := deps.newStore()
	if store == nil {
		return errors.New("MCP OAuth keyring is unavailable")
	}
	deleteErr := store.Delete(server.Name)
	if deleteErr != nil && !errors.Is(deleteErr, mcpoauth.ErrNotFound) {
		return deleteErr
	}
	if err := config.SetMCPOAuthEnabled(configPath, server.Name, false); err != nil {
		return fmt.Errorf("disable OAuth for MCP server %q: %w", server.Name, err)
	}
	if errors.Is(deleteErr, mcpoauth.ErrNotFound) {
		_, err = fmt.Fprintf(stdout, "No stored OAuth credential for MCP server %q; OAuth is disabled.\n", server.Name)
		return err
	}
	_, err = fmt.Fprintf(stdout, "Logged out of MCP server %q.\n", server.Name)
	return err
}

func configuredMCPServerForOAuth(configPath, name string) (config.MCPServerConfig, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return config.MCPServerConfig{}, err
	}
	name = strings.TrimSpace(name)
	for _, server := range cfg.MCP.Servers {
		if strings.TrimSpace(server.Name) != name {
			continue
		}
		if server.TransportType() != config.MCPTransportStreamableHTTP {
			return config.MCPServerConfig{}, fmt.Errorf("MCP server %q does not support OAuth", name)
		}
		if strings.TrimSpace(server.BearerTokenEnvVar) != "" {
			return config.MCPServerConfig{}, fmt.Errorf("MCP server %q uses bearer_token_env_var instead of OAuth", name)
		}
		server.Name = name
		server.URL = strings.TrimSpace(server.URL)
		return server, nil
	}
	return config.MCPServerConfig{}, fmt.Errorf("MCP server %q is not configured", name)
}

func openMCPAuthorizationURL(authorizationURL string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", authorizationURL)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", authorizationURL)
	default:
		command = exec.Command("xdg-open", authorizationURL)
	}
	return command.Start()
}
