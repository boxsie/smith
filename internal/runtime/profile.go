package runtime

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ProfileRequest struct {
	Name              string
	ExecutionProfile  string
	Context           []ContextReference
	Session           SessionPolicy
	WorkspaceMode     string
	WorkspaceRoot     string
	WritableRoots     []string
	RequireWriteGrant bool
	Capabilities      []string
	Limits            LimitPolicy
	Attempts          AttemptPolicy
}

func ResolveProfile(request ProfileRequest) (ResolvedProfile, error) {
	name := request.Name
	if name == "" {
		name = DefaultProfile
	}
	references := cloneContextReferences(request.Context)
	executionProfile, limits, err := ResolveExecutionProfile(request.ExecutionProfile, request.Limits)
	if err != nil {
		return ResolvedProfile{}, err
	}
	profile := ResolvedProfile{
		Name:             name,
		ExecutionProfile: executionProfile,
		Context:          ContextPolicy{Mode: ContextExplicit, SHA256: contextSHA256(references), References: references},
		Session:          request.Session,
		Limits:           limits,
	}
	profile.Attempts, err = ResolveAttemptPolicy(request.Attempts)
	if err != nil {
		return ResolvedProfile{}, fmt.Errorf("resolve attempt policy: %w", err)
	}
	if profile.Session.Mode == "" {
		profile.Session.Mode = SessionFresh
	}
	if err := validateSessionPolicy(profile.Session); err != nil {
		return ResolvedProfile{}, err
	}

	switch name {
	case CapabilityReason:
		if request.WorkspaceMode != "" && request.WorkspaceMode != WorkspaceNone {
			return ResolvedProfile{}, fmt.Errorf("reason profile cannot request workspace mode %q", request.WorkspaceMode)
		}
		profile.Capabilities = CapabilityPolicy{Profile: CapabilityReason}
		profile.Workspace = WorkspacePolicy{Access: WorkspaceNone, Isolation: "ephemeral", Granted: true}
	case CapabilityInspect:
		if request.WorkspaceMode != "" && request.WorkspaceMode != WorkspaceRoot {
			return ResolvedProfile{}, fmt.Errorf("inspect profile cannot request workspace mode %q", request.WorkspaceMode)
		}
		root, err := existingDirectory(request.WorkspaceRoot)
		if err != nil {
			return ResolvedProfile{}, fmt.Errorf("inspect workspace: %w", err)
		}
		profile.Capabilities = CapabilityPolicy{Profile: CapabilityInspect, Allow: []string{"workspace.inspect"}}
		profile.Workspace = WorkspacePolicy{Root: root, Access: WorkspaceReadOnly, Isolation: WorkspaceRoot, Granted: true}
	case CapabilityWork:
		mode := request.WorkspaceMode
		if mode == "" {
			mode = WorkspaceRoot
		}
		if mode != WorkspaceRoot && mode != WorkspaceWorktree {
			return ResolvedProfile{}, fmt.Errorf("work profile has unsupported workspace mode %q", mode)
		}
		root, err := existingDirectory(request.WorkspaceRoot)
		if err != nil {
			return ResolvedProfile{}, fmt.Errorf("work workspace: %w", err)
		}
		grant, granted := matchingWriteGrant(root, request.WritableRoots)
		if request.RequireWriteGrant && !granted {
			return ResolvedProfile{}, fmt.Errorf("work profile workspace %q is not inside a conductor-granted writable root", root)
		}
		if request.RequireWriteGrant && mode == WorkspaceWorktree {
			if err := validateLinkedWorktree(root); err != nil {
				return ResolvedProfile{}, err
			}
		}
		profile.Capabilities = CapabilityPolicy{Profile: CapabilityWork, Allow: []string{"workspace.inspect", "workspace.edit"}}
		profile.Workspace = WorkspacePolicy{Root: root, Access: WorkspaceWritable, Isolation: mode, Granted: granted, GrantRoot: grant}
	default:
		return ResolvedProfile{}, fmt.Errorf("unknown external runtime profile %q", name)
	}
	for _, capability := range request.Capabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			return ResolvedProfile{}, fmt.Errorf("external capability names cannot be empty")
		}
		if containsString(profile.Capabilities.Allow, capability) {
			return ResolvedProfile{}, fmt.Errorf("external capability %q is duplicated", capability)
		}
		profile.Capabilities.Allow = append(profile.Capabilities.Allow, capability)
	}
	sort.Strings(profile.Capabilities.Allow)
	return profile, nil
}

func cloneContextReferences(references []ContextReference) []ContextReference {
	result := make([]ContextReference, len(references))
	for index, reference := range references {
		result[index] = reference
		if reference.Metadata != nil {
			result[index].Metadata = make(map[string]string, len(reference.Metadata))
			for key, value := range reference.Metadata {
				result[index].Metadata[key] = value
			}
		}
	}
	return result
}

func contextSHA256(references []ContextReference) string {
	if len(references) == 0 {
		return ""
	}
	data, _ := json.Marshal(references)
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest)
}

func (p LimitPolicy) TimeoutDuration() (time.Duration, error) {
	if p.Timeout == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(p.Timeout)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q: %w", p.Timeout, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("timeout must be positive")
	}
	return duration, nil
}

func validateSessionPolicy(policy SessionPolicy) error {
	switch policy.Mode {
	case SessionFresh:
		if policy.ID != "" {
			return fmt.Errorf("fresh sessions cannot specify an id")
		}
	case SessionSticky:
	case SessionResume, SessionFork:
		if strings.TrimSpace(policy.ID) == "" {
			return fmt.Errorf("%s sessions require an id", policy.Mode)
		}
	default:
		return fmt.Errorf("unsupported session mode %q", policy.Mode)
	}
	return nil
}

func validateLimitPolicy(policy LimitPolicy) error {
	if _, err := policy.TimeoutDuration(); err != nil {
		return err
	}
	if _, err := policy.TerminationGraceDuration(); err != nil {
		return err
	}
	if policy.MaxTurns < 0 || policy.MaxOutputBytes < 0 || policy.MaxEvents < 0 ||
		policy.MaxMemoryBytes < 0 || policy.MaxProcesses < 0 || policy.CPUQuotaPercent < 0 || policy.MaxWorkspaceBytes < 0 {
		return fmt.Errorf("profile limits cannot be negative")
	}
	return nil
}

func existingDirectory(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}

func matchingWriteGrant(root string, grants []string) (string, bool) {
	for _, candidate := range grants {
		grant, err := existingDirectory(candidate)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(grant, root)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return grant, true
		}
	}
	return "", false
}

func validateProfileWorkspace(profile string, workspace WorkspacePolicy) error {
	switch profile {
	case CapabilityReason:
		if workspace.Access != WorkspaceNone {
			return fmt.Errorf("reason profile requires no workspace access")
		}
	case CapabilityInspect:
		if workspace.Access != WorkspaceReadOnly || workspace.Root == "" || !filepath.IsAbs(workspace.Root) {
			return fmt.Errorf("inspect profile requires an absolute read-only workspace")
		}
	case CapabilityWork:
		if workspace.Access != WorkspaceWritable || workspace.Root == "" || !filepath.IsAbs(workspace.Root) || !workspace.Granted {
			return fmt.Errorf("work profile requires an explicitly granted writable workspace")
		}
	default:
		return fmt.Errorf("unknown profile %q", profile)
	}
	return nil
}

func validateProfileCapabilities(profile string, capabilities CapabilityPolicy, servers []MCPServer) error {
	want := []string(nil)
	switch profile {
	case CapabilityInspect:
		want = []string{"workspace.inspect"}
	case CapabilityWork:
		want = []string{"workspace.inspect", "workspace.edit"}
	}
	for _, server := range servers {
		if server.Capability == "" || server.Name == "" || server.URL == "" || len(server.Tools) == 0 {
			return fmt.Errorf("MCP capability servers require capability, name, URL, and tools")
		}
		want = append(want, server.Capability)
	}
	if capabilities.Profile != "" && capabilities.Profile != profile {
		return fmt.Errorf("capability profile %q does not match runtime profile %q", capabilities.Profile, profile)
	}
	if !sameStringSet(capabilities.Allow, want) || len(capabilities.Deny) > 0 {
		return fmt.Errorf("%s profile capabilities do not match its named policy", profile)
	}
	return nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func validateLinkedWorktree(root string) error {
	data, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		return fmt.Errorf("isolated worktree %q has no linked-worktree .git file", root)
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return fmt.Errorf("isolated worktree %q has an invalid .git file", root)
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	info, err := os.Stat(filepath.Clean(gitDir))
	if err != nil || !info.IsDir() {
		return fmt.Errorf("isolated worktree %q points to a missing git directory", root)
	}
	return nil
}
