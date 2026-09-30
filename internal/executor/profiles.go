package executor

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
)

func ResolveExternalProfiles(root *task.Task, workspaceRoot string, writableRoots []string, requireWriteGrant bool) (map[string]runtime.ResolvedProfile, error) {
	profiles := make(map[string]runtime.ResolvedProfile)
	var resolveErr error
	task.WalkTree(root, func(current *task.Task) {
		if resolveErr != nil || current.EffectiveAgent.Runtime == "" || current.EffectiveAgent.Runtime == runtime.ProviderRuntime {
			return
		}
		session := runtime.SessionPolicy{}
		if current.EffectiveAgent.Session != nil {
			session = *current.EffectiveAgent.Session
		}
		limits := runtime.LimitPolicy{}
		if current.EffectiveAgent.Limits != nil {
			limits = *current.EffectiveAgent.Limits
		}
		profile, err := runtime.ResolveProfile(runtime.ProfileRequest{
			Name:              current.EffectiveAgent.Profile,
			ExecutionProfile:  current.EffectiveAgent.ExecutionProfile,
			Context:           externalContext(current),
			Session:           session,
			WorkspaceMode:     current.EffectiveAgent.Workspace,
			WorkspaceRoot:     workspaceRoot,
			WritableRoots:     writableRoots,
			RequireWriteGrant: requireWriteGrant,
			Limits:            limits,
			Attempts:          attemptPolicy(current.EffectiveAgent.Attempts),
		})
		if err != nil {
			resolveErr = fmt.Errorf("task %q: resolve external profile: %w", taskLabel(current), err)
			return
		}
		profiles[current.ID] = profile
	})
	return profiles, resolveErr
}

func attemptPolicy(policy *runtime.AttemptPolicy) runtime.AttemptPolicy {
	if policy == nil {
		return runtime.AttemptPolicy{}
	}
	cloned := *policy
	cloned.RetryableReasons = append([]string(nil), policy.RetryableReasons...)
	return cloned
}

func taskLabel(value *task.Task) string {
	if value.ID == "" {
		return "root"
	}
	return value.ID
}

type stickySessionState struct {
	SessionID string `json:"session_id"`
}

func loadStickySession(appRoot string, current *task.Task, profile runtime.ResolvedProfile) (string, error) {
	path := stickySessionPath(appRoot, current, profile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read sticky session: %w", err)
	}
	var state stickySessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return "", fmt.Errorf("parse sticky session: %w", err)
	}
	return state.SessionID, nil
}

func saveStickySession(appRoot string, current *task.Task, profile runtime.ResolvedProfile, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("external runtime completed a sticky session without a session id")
	}
	path := stickySessionPath(appRoot, current, profile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create sticky session directory: %w", err)
	}
	data, err := json.Marshal(stickySessionState{SessionID: sessionID})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".session-*.json")
	if err != nil {
		return fmt.Errorf("create sticky session state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("replace sticky session state: %w", err)
		}
		if retryErr := os.Rename(temporaryPath, path); retryErr != nil {
			return fmt.Errorf("replace sticky session state: %w", retryErr)
		}
	}
	return nil
}

func stickySessionPath(appRoot string, current *task.Task, profile runtime.ResolvedProfile) string {
	identity := struct {
		TaskID       string                   `json:"task_id"`
		Runtime      string                   `json:"runtime"`
		Model        string                   `json:"model"`
		Persona      string                   `json:"persona"`
		Profile      string                   `json:"profile"`
		Context      runtime.ContextPolicy    `json:"context"`
		Capabilities runtime.CapabilityPolicy `json:"capabilities"`
		Workspace    runtime.WorkspacePolicy  `json:"workspace"`
	}{
		TaskID: current.ID, Runtime: current.EffectiveAgent.Runtime, Model: current.EffectiveAgent.Model,
		Persona: current.EffectiveAgent.Persona, Profile: profile.Name, Context: profile.Context,
		Capabilities: profile.Capabilities, Workspace: profile.Workspace,
	}
	identity.Workspace.Granted = false
	identity.Workspace.GrantRoot = ""
	profileData, _ := json.Marshal(identity)
	key := string(profileData)
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(appRoot, ".smith", "sessions", fmt.Sprintf("%x.json", digest))
}
