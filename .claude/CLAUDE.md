# Coding Style & Behavioral Guidelines

## Style Requirements
- Follow Golang, Javascript and HTMX idiomatic patterns.
- Keep functions small and modular.
- Keep front-end bundle as small as possible.

## Destructive Actions & Refactoring Safety
- DO NOT silently remove, delete, or replace existing functions, classes, or obsolete code blocks.
- ASK FOR CONFIRMATION explicitly before deleting or replacing any legacy/old code:
  - Explain what code will be removed.
  - Explain why it is being replaced.
  - Wait for user approval before carrying out the deletion.