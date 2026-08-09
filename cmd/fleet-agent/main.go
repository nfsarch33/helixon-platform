// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
// Command fleet-agent is the Helixon fleet remote-control daemon + installer
// (v0.5.0, replaces the v0.1.0 shell-based installer).
//
// Subcommands:
//
//	install    one-shot install + enable (idempotent)
//	serve      run the long-lived control plane (HTTP + heartbeat + gRPC stub)
//	doctor     run the doctor suite against this node
//	register   announce this node to svcregistryd
//	version    print version and exit
//
// This binary is intentionally thin: it defers logic to internal/fleetagent.
//
// Why HTTP first instead of pure gRPC: matches the existing svcregistryd
// HTTPServer pattern (internal/svcregistry/http.go) so on-call tooling
// (curl, registra health) can probe without grpc-cli. gRPC interop is
// added in a follow-up PR once v0.5.0 lands.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nfsarch33/helixon-platform/internal/fleetagent"
	"github.com/spf13/cobra"
)

const version = "helixon-fleet-agent v0.5.0-dev (Sprint v18750-O)"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	root := &cobra.Command{
		Use:           "fleet-agent",
		Short:         "Helixon fleet remote-control daemon + installer (v0.5.0)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newInstallCmd(logger))
	root.AddCommand(newServeCmd(logger))
	root.AddCommand(newDoctorCmd(logger))
	root.AddCommand(newRegisterCmd(logger))
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "print version and exit",
		Run: func(_ *cobra.Command, _ []string) { fmt.Println(version) },
	})

	if err := root.ExecuteContext(signalContext()); err != nil {
		fmt.Fprintln(os.Stderr, "fleet-agent:", err)
		os.Exit(1)
	}
}

func signalContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx
}

func newInstallCmd(logger *slog.Logger) *cobra.Command {
	var (
		configPath  string
		registryURL string
		force       bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "one-shot install + enable (idempotent)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := fleetagent.InstallOptions{
				ConfigPath:  configPath,
				RegistryURL: registryURL,
				Force:       force,
				Logger:      logger,
			}
			res, err := fleetagent.Install(cmd.Context(), opts)
			if err != nil {
				return fmt.Errorf("install: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), fleetagent.EnvelopeJSON(res))
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", fleetagent.DefaultConfigPath, "agent.toml path")
	cmd.Flags().StringVar(&registryURL, "registry", fleetagent.DefaultRegistryURL, "svcregistryd base URL")
	cmd.Flags().BoolVar(&force, "force", false, "re-create host key if one already exists")
	return cmd
}

func newServeCmd(logger *slog.Logger) *cobra.Command {
	var (
		configPath  string
		httpAddr    string
		registryURL string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "run the long-lived control plane (HTTP + heartbeat)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := fleetagent.ServeOptions{
				ConfigPath:  configPath,
				HTTPAddr:    httpAddr,
				RegistryURL: registryURL,
				Logger:      logger,
			}
			if err := fleetagent.Serve(cmd.Context(), opts); err != nil &&
				!errors.Is(err, context.Canceled) {
				return fmt.Errorf("serve: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", fleetagent.DefaultConfigPath, "agent.toml path")
	cmd.Flags().StringVar(&httpAddr, "http-addr", ":8686", "control HTTP listen address")
	cmd.Flags().StringVar(&registryURL, "registry", fleetagent.DefaultRegistryURL, "svcregistryd base URL")
	return cmd
}

func newDoctorCmd(logger *slog.Logger) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "run the doctor suite against this node",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := fleetagent.DoctorOptions{
				ConfigPath: configPath,
				Logger:     logger,
			}
			res, err := fleetagent.Doctor(cmd.Context(), opts)
			if err != nil {
				return fmt.Errorf("doctor: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), fleetagent.DoctorReport(res))
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", fleetagent.DefaultConfigPath, "agent.toml path")
	return cmd
}

func newRegisterCmd(logger *slog.Logger) *cobra.Command {
	var (
		configPath  string
		registryURL string
	)
	cmd := &cobra.Command{
		Use:   "register",
		Short: "announce this node to svcregistryd",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := fleetagent.RegisterOptions{
				ConfigPath:  configPath,
				RegistryURL: registryURL,
				Logger:      logger,
			}
			if err := fleetagent.Register(cmd.Context(), opts); err != nil {
				return fmt.Errorf("register: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", fleetagent.DefaultConfigPath, "agent.toml path")
	cmd.Flags().StringVar(&registryURL, "registry", fleetagent.DefaultRegistryURL, "svcregistryd base URL")
	return cmd
}
