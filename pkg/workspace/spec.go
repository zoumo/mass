// Package workspace defines the OAR Workspace Specification types.
// These types model workspace.json (the workspace preparation configuration) and
// mirror the schema defined in docs/design/workspace/workspace-spec.md.
package workspace

import (
	"encoding/json"
	"fmt"
)

// WorkspaceSpec is the top-level OAR Workspace Specification structure.
// It describes how to prepare an agent's working environment.
type WorkspaceSpec struct {
	// OarVersion is the version of the OAR Workspace Specification (SemVer).
	// Consumers MUST reject unknown major versions.
	OarVersion string `json:"oarVersion"`

	// Metadata describes the identity of this workspace.
	Metadata WorkspaceMetadata `json:"metadata"`

	// Source describes where the code comes from (git, emptyDir, or local).
	// REQUIRED. Must be a valid Source with a recognized type.
	Source Source `json:"source"`

	// Hooks specifies workspace lifecycle hooks (setup and teardown).
	// Optional — hooks are not required for basic workspace preparation.
	Hooks Hooks `json:"hooks,omitempty"`
}

// WorkspaceMetadata describes the identity of a workspace.
type WorkspaceMetadata struct {
	// Name is the workspace name (e.g. "auth-refactor-workspace").
	// REQUIRED. Must not be empty.
	Name string `json:"name"`

	// Annotations contains arbitrary key-value metadata.
	// Keys SHOULD use reverse-domain notation (e.g. "org.openagents.workspace.language").
	Annotations map[string]string `json:"annotations,omitempty"`
}

// SourceType constants define the recognized source type values.
type SourceType string

const (
	// SourceTypeGit indicates a git repository source.
	SourceTypeGit SourceType = "git"

	// SourceTypeEmptyDir indicates an empty directory source.
	SourceTypeEmptyDir SourceType = "emptyDir"

	// SourceTypeLocal indicates a local directory source.
	SourceTypeLocal SourceType = "local"
)

// IsValid reports whether st is a known SourceType value.
func (st SourceType) IsValid() bool {
	switch st {
	case SourceTypeGit, SourceTypeEmptyDir, SourceTypeLocal:
		return true
	}
	return false
}

// String implements fmt.Stringer.
func (st SourceType) String() string {
	return string(st)
}

// Source is a discriminated union representing the workspace source.
// The Type field determines which concrete source type is active:
//   - "git"    → GitSource (url, ref, depth)
//   - "emptyDir" → EmptyDirSource (no fields)
//   - "local"  → LocalSource (path)
//
// Use Source.Git, Source.EmptyDir, or Source.Local to access the concrete fields
// after parsing. Only one will be populated based on the Type value.
type Source struct {
	// Type is the source type discriminator. REQUIRED.
	// Must be one of: "git", "emptyDir", "local".
	Type SourceType `json:"type"`

	// Git contains the git source configuration when Type == "git".
	Git GitSource `json:"-"` // excluded from direct JSON marshal

	// EmptyDir contains the empty directory source configuration when Type == "emptyDir".
	EmptyDir EmptyDirSource `json:"-"` // excluded from direct JSON marshal

	// Local contains the local directory source configuration when Type == "local".
	Local LocalSource `json:"-"` // excluded from direct JSON marshal
}

// GitSource describes a git repository source.
type GitSource struct {
	// URL is the git repository URL. REQUIRED.
	// Must be a valid git URL (https://, git://, ssh://, or user@host:path).
	URL string `json:"url"`

	// Ref is the git reference to checkout (branch, tag, or commit SHA).
	// Optional — defaults to the repository's default branch.
	Ref string `json:"ref,omitempty"`

	// Depth is the shallow clone depth.
	// 0 or omitted means full clone (all history).
	// > 0 means shallow clone with that depth.
	Depth int `json:"depth,omitempty"`
}

// EmptyDirSource describes an empty directory source.
// No fields — agentd creates an empty managed directory.
type EmptyDirSource struct{}

// LocalSource describes a local directory source.
type LocalSource struct {
	// Path is the absolute path to an existing directory on the host.
	// REQUIRED. Must be an absolute path.
	Path string `json:"path"`
}

// Hook describes a single workspace lifecycle hook command.
type Hook struct {
	// Command is the executable to run. REQUIRED.
	// Must be a valid executable name or path.
	Command string `json:"command"`

	// Args are the command-line arguments passed to Command.
	// Optional — can be empty for commands that take no arguments.
	Args []string `json:"args,omitempty"`

	// Description is a human-readable description for logging.
	// Optional — used for documentation and log output.
	Description string `json:"description,omitempty"`
}

// Hooks specifies workspace lifecycle hooks.
// Hooks run in array order. Any hook failure aborts the workspace preparation.
type Hooks struct {
	// Setup hooks run after source preparation (git clone or directory creation).
	// Commands run with the workspace directory as their working directory.
	Setup []Hook `json:"setup,omitempty"`

	// Teardown hooks run before workspace destruction.
	// Commands run with the workspace directory as their working directory.
	Teardown []Hook `json:"teardown,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling for Source.
// It parses the type discriminator first, then unmarshals the remaining
// fields into the appropriate concrete source type.
func (s *Source) UnmarshalJSON(data []byte) error {
	// First, parse just the type field to determine which concrete type to use.
	type sourceTypeOnly struct {
		Type SourceType `json:"type"`
	}
	var sto sourceTypeOnly
	if err := json.Unmarshal(data, &sto); err != nil {
		return fmt.Errorf("workspace: parse source type: %w", err)
	}

	s.Type = sto.Type

	// Now unmarshal the full data into the appropriate concrete type.
	switch s.Type {
	case SourceTypeGit:
		if err := json.Unmarshal(data, &s.Git); err != nil {
			return fmt.Errorf("workspace: parse git source: %w", err)
		}
	case SourceTypeEmptyDir:
		// EmptyDir has no fields — nothing to parse.
		// We still accept extra fields for forward compatibility.
	case SourceTypeLocal:
		if err := json.Unmarshal(data, &s.Local); err != nil {
			return fmt.Errorf("workspace: parse local source: %w", err)
		}
	default:
		return fmt.Errorf("workspace: unknown source type %q", s.Type)
	}

	return nil
}

// MarshalJSON implements custom JSON marshaling for Source.
// It marshals the appropriate concrete source type based on the Type field.
func (s Source) MarshalJSON() ([]byte, error) {
	switch s.Type {
	case SourceTypeGit:
		// Create an intermediate struct that includes type + git fields.
		type gitSourceJSON struct {
			Type   SourceType `json:"type"`
			URL    string     `json:"url"`
			Ref    string     `json:"ref,omitempty"`
			Depth  int        `json:"depth,omitempty"`
		}
		return json.Marshal(gitSourceJSON{
			Type:  s.Type,
			URL:   s.Git.URL,
			Ref:   s.Git.Ref,
			Depth: s.Git.Depth,
		})
	case SourceTypeEmptyDir:
		// Only marshal the type field.
		type emptyDirSourceJSON struct {
			Type SourceType `json:"type"`
		}
		return json.Marshal(emptyDirSourceJSON{Type: s.Type})
	case SourceTypeLocal:
		// Create an intermediate struct that includes type + local fields.
		type localSourceJSON struct {
			Type SourceType `json:"type"`
			Path string     `json:"path"`
		}
		return json.Marshal(localSourceJSON{
			Type: s.Type,
			Path: s.Local.Path,
		})
	default:
		return nil, fmt.Errorf("workspace: cannot marshal unknown source type %q", s.Type)
	}
}

// ParseWorkspaceSpec parses JSON data into a WorkspaceSpec.
// Returns an error if the JSON is malformed or if parsing fails.
func ParseWorkspaceSpec(data []byte) (WorkspaceSpec, error) {
	var spec WorkspaceSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return WorkspaceSpec{}, fmt.Errorf("workspace: parse spec: %w", err)
	}
	return spec, nil
}

// ValidateWorkspaceSpec checks that spec satisfies the OAR Workspace Specification
// validation rules:
//   - oarVersion must be non-empty and have major version == 0
//   - metadata.name must be non-empty
//   - source.type must be a recognized SourceType value
//   - source.git.url must be non-empty when type == "git"
//   - source.local.path must be non-empty and absolute when type == "local"
//   - hooks.setup and hooks.teardown commands must be non-empty
func ValidateWorkspaceSpec(spec WorkspaceSpec) error {
	// Validate oarVersion.
	if spec.OarVersion == "" {
		return fmt.Errorf("workspace: oarVersion is required")
	}
	major, err := parseMajor(spec.OarVersion)
	if err != nil {
		return fmt.Errorf("workspace: oarVersion %q is not a valid SemVer: %w", spec.OarVersion, err)
	}
	if major != 0 {
		return fmt.Errorf("workspace: unsupported oarVersion major %d (only 0.x.x is supported)", major)
	}

	// Validate metadata.name.
	if spec.Metadata.Name == "" {
		return fmt.Errorf("workspace: metadata.name is required")
	}

	// Validate source.type.
	if !spec.Source.Type.IsValid() {
		return fmt.Errorf("workspace: source.type %q is not valid (valid: git, emptyDir, local)", spec.Source.Type)
	}

	// Validate source fields based on type.
	switch spec.Source.Type {
	case SourceTypeGit:
		if spec.Source.Git.URL == "" {
			return fmt.Errorf("workspace: source.url is required for git source")
		}
	case SourceTypeLocal:
		if spec.Source.Local.Path == "" {
			return fmt.Errorf("workspace: source.path is required for local source")
		}
		if !isAbs(spec.Source.Local.Path) {
			return fmt.Errorf("workspace: source.path must be an absolute path for local source, got %q", spec.Source.Local.Path)
		}
	case SourceTypeEmptyDir:
		// No additional validation for emptyDir.
	}

	// Validate hooks.
	for i, h := range spec.Hooks.Setup {
		if h.Command == "" {
			return fmt.Errorf("workspace: hooks.setup[%d].command is required", i)
		}
	}
	for i, h := range spec.Hooks.Teardown {
		if h.Command == "" {
			return fmt.Errorf("workspace: hooks.teardown[%d].command is required", i)
		}
	}

	return nil
}

// parseMajor extracts the major version integer from a SemVer string.
// Accepts "MAJOR.MINOR.PATCH" and "MAJOR.MINOR.PATCH-pre+build" forms.
func parseMajor(version string) (int, error) {
	// Strip any build metadata or pre-release suffix after the first '-' or '+'.
	core := version
	for i := 0; i < len(core); i++ {
		if core[i] == '-' || core[i] == '+' {
			core = core[:i]
			break
		}
	}

	// Split by '.' and extract major.
	parts := splitN(core, '.', 3)
	if len(parts) < 1 || parts[0] == "" {
		return 0, fmt.Errorf("empty major component")
	}

	major := 0
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-numeric major %q", parts[0])
		}
		major = major*10 + int(c-'0')
	}

	return major, nil
}

// splitN splits a string by a delimiter, returning at most n parts.
// Simple implementation to avoid importing strings package in validation code.
func splitN(s string, delim byte, n int) []string {
	if n <= 0 {
		return nil
	}
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == delim {
			parts = append(parts, s[start:i])
			start = i + 1
			if len(parts) == n-1 {
				break
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// isAbs checks if a path is absolute.
// Simple implementation to avoid importing path/filepath for pure validation.
func isAbs(path string) bool {
	if len(path) == 0 {
		return false
	}
	// Unix absolute path starts with '/'.
	// Windows absolute path starts with drive letter (e.g., "C:\").
	// For cross-platform support, we check both.
	return path[0] == '/' || (len(path) >= 2 && path[1] == ':')
}