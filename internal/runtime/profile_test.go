package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestResolveProfileDefaultsToSterileFreshReason(t *testing.T) {
	profile, err := ResolveProfile(ProfileRequest{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if profile.Name != CapabilityReason || profile.Context.Mode != ContextExplicit || profile.Session.Mode != SessionFresh {
		t.Fatalf("profile = %#v", profile)
	}
	if profile.Workspace.Access != WorkspaceNone || profile.Workspace.Isolation != "ephemeral" || !profile.Workspace.Granted {
		t.Fatalf("workspace = %#v", profile.Workspace)
	}
	if len(profile.Capabilities.Allow) != 0 {
		t.Fatalf("reason capabilities = %#v", profile.Capabilities)
	}
}

func TestResolveProfileContextHashTracksSuppliedArtifactVersion(t *testing.T) {
	first, err := ResolveProfile(ProfileRequest{Name: CapabilityReason, Context: []ContextReference{{Name: "persona", URI: "/memory", SHA256: "first", Source: "memory"}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveProfile(ProfileRequest{Name: CapabilityReason, Context: []ContextReference{{Name: "persona", URI: "/memory", SHA256: "second", Source: "memory"}}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Context.SHA256 == "" || second.Context.SHA256 == "" || first.Context.SHA256 == second.Context.SHA256 {
		t.Fatalf("context hashes = %q / %q", first.Context.SHA256, second.Context.SHA256)
	}
}

func TestProfileCarriesExplicitExternalCapabilities(t *testing.T) {
	profile, err := ResolveProfile(ProfileRequest{Name: CapabilityReason, Capabilities: []string{"tickets_please.read"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(profile.Capabilities.Allow, []string{"tickets_please.read"}) {
		t.Fatalf("capabilities = %v", profile.Capabilities.Allow)
	}
	if _, err := ResolveProfile(ProfileRequest{Name: CapabilityReason, Capabilities: []string{"tickets_please.read", "tickets_please.read"}}); err == nil {
		t.Fatal("duplicate external capability accepted")
	}
}

func TestResolveProfileInspectAndWorkAuthority(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}

	inspect, err := ResolveProfile(ProfileRequest{Name: CapabilityInspect, WorkspaceRoot: child})
	if err != nil {
		t.Fatalf("resolve inspect: %v", err)
	}
	if inspect.Workspace.Access != WorkspaceReadOnly || !inspect.Workspace.Granted || inspect.Workspace.Root != child {
		t.Fatalf("inspect workspace = %#v", inspect.Workspace)
	}

	if _, err := ResolveProfile(ProfileRequest{Name: CapabilityWork, WorkspaceRoot: child, RequireWriteGrant: true}); err == nil {
		t.Fatal("work profile resolved without a conductor grant")
	}
	work, err := ResolveProfile(ProfileRequest{Name: CapabilityWork, WorkspaceRoot: child, WritableRoots: []string{root}, RequireWriteGrant: true})
	if err != nil {
		t.Fatalf("resolve work: %v", err)
	}
	if work.Workspace.Access != WorkspaceWritable || !work.Workspace.Granted || work.Workspace.GrantRoot != root {
		t.Fatalf("work workspace = %#v", work.Workspace)
	}
}

func TestResolveProfileRejectsEscapedWriteGrantAndInvalidDeclarations(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	tests := []ProfileRequest{
		{Name: CapabilityWork, WorkspaceRoot: outside, WritableRoots: []string{root}, RequireWriteGrant: true},
		{Name: CapabilityReason, WorkspaceMode: WorkspaceRoot},
		{Name: CapabilityInspect, WorkspaceRoot: root, Session: SessionPolicy{Mode: SessionFresh, ID: "ambient"}},
		{Name: CapabilityInspect, WorkspaceRoot: root, Limits: LimitPolicy{Timeout: "eventually"}},
		{Name: CapabilityInspect, WorkspaceRoot: root, Limits: LimitPolicy{MaxMemoryBytes: -1}},
		{Name: CapabilityInspect, WorkspaceRoot: root, Limits: LimitPolicy{MaxProcesses: -1}},
		{Name: "admin"},
	}
	for index, request := range tests {
		if _, err := ResolveProfile(request); err == nil {
			t.Errorf("case %d unexpectedly resolved", index)
		}
	}
}

func TestResolveProfileSupportsExplicitSessionModesAndLimits(t *testing.T) {
	root := t.TempDir()
	for _, mode := range []string{SessionSticky, SessionResume, SessionFork} {
		session := SessionPolicy{Mode: mode}
		if mode != SessionSticky {
			session.ID = "source-session"
		}
		profile, err := ResolveProfile(ProfileRequest{
			Name:          CapabilityInspect,
			WorkspaceRoot: root,
			Session:       session,
			Limits:        LimitPolicy{Timeout: "2m", MaxTurns: 4, MaxOutputBytes: 4096, MaxEvents: 20, MaxMemoryBytes: 1 << 30, MaxProcesses: 64},
		})
		if err != nil {
			t.Fatalf("resolve %s: %v", mode, err)
		}
		if profile.Session.Mode != mode || profile.Limits.MaxTurns != 4 ||
			profile.Limits.MaxMemoryBytes != 1<<30 || profile.Limits.MaxProcesses != 64 {
			t.Fatalf("profile = %#v", profile)
		}
	}
}

func TestResolveProfileAppliesConservativeProcessDefaults(t *testing.T) {
	profile, err := ResolveProfile(ProfileRequest{Name: CapabilityReason})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Limits.MaxMemoryBytes != DefaultExternalMemoryBytes || profile.Limits.MaxProcesses != DefaultExternalProcesses {
		t.Fatalf("default limits = %#v", profile.Limits)
	}
}

func TestResolveProfileRequiresARealLinkedWorktreeWhenGranted(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveProfile(ProfileRequest{Name: CapabilityWork, WorkspaceMode: WorkspaceWorktree, WorkspaceRoot: root, WritableRoots: []string{root}, RequireWriteGrant: true}); err == nil {
		t.Fatal("ordinary directory accepted as an isolated worktree")
	}
	gitDir := filepath.Join(t.TempDir(), "worktrees", "smith-node")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile, err := ResolveProfile(ProfileRequest{Name: CapabilityWork, WorkspaceMode: WorkspaceWorktree, WorkspaceRoot: root, WritableRoots: []string{root}, RequireWriteGrant: true})
	if err != nil {
		t.Fatalf("resolve linked worktree: %v", err)
	}
	if profile.Workspace.Isolation != WorkspaceWorktree || !profile.Workspace.Granted {
		t.Fatalf("workspace = %#v", profile.Workspace)
	}
}

func TestNamedProfilesMapEquivalentIntentToClaudeAndCodex(t *testing.T) {
	root := t.TempDir()
	profiles := []struct {
		name             string
		workspace        WorkspacePolicy
		claudeTools      string
		claudePermission string
		codexSandbox     string
		codexShell       bool
	}{
		{name: CapabilityReason, workspace: WorkspacePolicy{Access: WorkspaceNone, Granted: true}, claudeTools: "", claudePermission: "plan", codexSandbox: "read-only"},
		{name: CapabilityInspect, workspace: WorkspacePolicy{Root: root, Access: WorkspaceReadOnly, Granted: true}, claudeTools: "Read,Glob,Grep", claudePermission: "plan", codexSandbox: "read-only", codexShell: true},
		{name: CapabilityWork, workspace: WorkspacePolicy{Root: root, Access: WorkspaceWritable, Granted: true, GrantRoot: root}, claudeTools: "Read,Glob,Grep,Edit,Write", claudePermission: "acceptEdits", codexSandbox: "workspace-write", codexShell: true},
	}
	for _, test := range profiles {
		t.Run(test.name, func(t *testing.T) {
			invocation := Invocation{Profile: test.name, Workspace: test.workspace, Session: SessionPolicy{Mode: SessionFresh}}
			claude := claudeArgs(invocation)
			if value := testArgValue(claude, "--tools"); value != test.claudeTools {
				t.Fatalf("claude tools = %q", value)
			}
			if value := testArgValue(claude, "--permission-mode"); value != test.claudePermission {
				t.Fatalf("claude permission = %q", value)
			}

			codex := codexArgs(invocation, "/tmp/schema.json", "/usr/bin")
			if value := testArgValue(codex, "--sandbox"); value != test.codexSandbox {
				t.Fatalf("codex sandbox = %q", value)
			}
			disablesShell := false
			for index, value := range codex {
				if value == "--disable" && index+1 < len(codex) && codex[index+1] == "shell_tool" {
					disablesShell = true
				}
			}
			if disablesShell == test.codexShell {
				t.Fatalf("codex shell mapping = %v, args %v", disablesShell, codex)
			}
			if !slices.Contains(codex, "--ignore-user-config") || !slices.Contains(codex, "--ignore-rules") {
				t.Fatalf("codex ambient config controls missing: %v", codex)
			}
		})
	}
}

func testArgValue(args []string, key string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key {
			return args[index+1]
		}
	}
	return ""
}
