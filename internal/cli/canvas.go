package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/boxsie/smith/internal/canvas"
	"github.com/boxsie/smith/internal/service"
	"github.com/spf13/cobra"
)

var canvasCmd = &cobra.Command{
	Use:           "canvas [patch]",
	Short:         "Open the local live patch canvas",
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := "."
		if len(args) == 1 {
			root = args[0]
		}
		root, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		address, _ := cmd.Flags().GetString("listen")
		if !loopbackListenAddress(address) {
			return fmt.Errorf("canvas listen address must be loopback")
		}
		writableRoots, _ := cmd.Flags().GetStringArray("writable-root")
		projectRoots, _ := cmd.Flags().GetStringArray("project-root")
		libraryRoot, _ := cmd.Flags().GetString("library-root")
		configPath, _ := cmd.Flags().GetString("harness-config")
		workAppURL, _ := cmd.Flags().GetString("work-app-url")
		memoryAppURL, _ := cmd.Flags().GetString("memory-app-url")
		var harnessConfig *service.HarnessConfig
		if configPath != "" {
			data, err := os.ReadFile(configPath)
			if err != nil {
				return err
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&harnessConfig); err != nil {
				return err
			}
			if harnessConfig == nil {
				return fmt.Errorf("harness configuration cannot be null")
			}
			if err := decoder.Decode(&struct{}{}); err != io.EOF {
				return fmt.Errorf("harness configuration must contain one JSON object")
			}
		}
		server, err := canvas.New(canvas.Config{Service: smithService, Root: root, WritableRoots: writableRoots, Harness: harnessConfig, ProjectRoots: projectRoots, LibraryRoot: libraryRoot, WorkAppURL: workAppURL, MemoryAppURL: memoryAppURL})
		if err != nil {
			return err
		}
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return err
		}
		defer func() { _ = listener.Close() }()
		urlHost := listener.Addr().String()
		if host, port, splitErr := net.SplitHostPort(urlHost); splitErr == nil && host == "::1" {
			urlHost = net.JoinHostPort("localhost", port)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "smith canvas listening at http://%s\n", urlHost)
		return server.Serve(cmd.Context(), listener)
	},
}

func init() {
	canvasCmd.Flags().String("work-app-url", "", "browser URL for tickets_please (not its MCP endpoint)")
	canvasCmd.Flags().String("memory-app-url", "", "browser URL for the memory renderer (not its API endpoint)")
	canvasCmd.Flags().String("listen", "127.0.0.1:7331", "loopback address for the canvas")
	canvasCmd.Flags().StringArray("writable-root", nil, "grant work-profile nodes a writable root (repeatable)")
	canvasCmd.Flags().String("harness-config", "", "operator-owned JSON harness configuration for prepare/start from a work ticket")
	canvasCmd.Flags().StringArray("project-root", nil, "mount a local project path or name=path for authoring/history (repeatable; does not grant model writes)")
	canvasCmd.Flags().String("library-root", "", "directory for new local projects (default: <patch>/.smith/library)")
	rootCmd.AddCommand(canvasCmd)
}

func loopbackListenAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
}
