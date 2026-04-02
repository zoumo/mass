---
estimated_steps: 1
estimated_files: 2
skills_used: []
---

# T01: Define WorkspaceSpec types with parsing and validation

Define WorkspaceSpec, Source (discriminated union with type field), GitSource, EmptyDirSource, LocalSource, Hook, Hooks types with JSON tags matching design doc schema. Add SourceType constants (SourceTypeGit, SourceTypeEmptyDir, SourceTypeLocal) with IsValid() method. Implement ParseWorkspaceSpec(data []byte) and ValidateWorkspaceSpec(spec) functions. Implement custom UnmarshalJSON for Source to handle discriminated union cleanly. Follow pkg/spec/types.go pattern: JSON tags, typed constants, validation methods, doc comments.

## Inputs

- ``docs/design/workspace/workspace-spec.md``
- ``pkg/spec/types.go``
- ``pkg/spec/config.go``
- ``pkg/spec/config_test.go``

## Expected Output

- ``pkg/workspace/spec.go``
- ``pkg/workspace/spec_test.go``

## Verification

go test ./pkg/workspace/... -run Spec

## Observability Impact

None — this task defines pure data types with no runtime behavior.
