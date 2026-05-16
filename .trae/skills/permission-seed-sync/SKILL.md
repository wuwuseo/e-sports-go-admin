---
name: "permission-seed-sync"
description: "Synchronize permission seed data from TypeScript permission-seed.ts to Go backend service. Invoke when user needs to sync permission definitions from frontend to backend or update seed data."
---

# Permission Seed Sync

This skill synchronizes permission seed data from a TypeScript `permission-seed.ts` file to the Go backend service's `system_permission.go` file.

## When to Use

- When user asks to sync/update permission seed data from frontend to backend
- When `permission-seed.ts` file has been modified and needs to be reflected in the backend
- When initializing or updating RBAC permission definitions

## Usage Steps

1. **Read the source permission-seed.ts file**
   - Default path: `../admin-web/web/src/data/permission-seed.ts`
   - Parse all permission entries (menu, api, button types)

2. **Read the target Go file**
   - Path: `internal/service/system_permission.go`
   - Locate the `InitSystemPermissions` function and its `seedData` slice

3. **Parse and transform data**
   - Extract all permission objects from TypeScript array
   - Transform each object to match Go `SeedPermission` struct format
   - Preserve parent-child relationships via ParentID field

4. **Update the Go file**
   - Replace the existing `seedData` slice content
   - Ensure proper Go syntax formatting
   - Maintain int8 type for Status field

5. **Verify compilation**
   - Run `go build` to verify no syntax errors

## Data Structure Mapping

| TypeScript Field | Go Field | Notes |
|-----------------|----------|-------|
| id | ID | int |
| domain | Domain | string |
| type | Type | string ("menu"/"api"/"button") |
| name | Name | string |
| code | Code | string |
| path | Path | string (for api) |
| method | Method | string (for api) |
| menu_path | MenuPath | string (for menu) |
| icon | Icon | string (for menu) |
| component | Component | string (for menu) |
| parent_id | ParentID | int |
| sort_order | SortOrder | int |
| is_common | IsCommon | bool |
| status | Status | int8 (1 = enabled) |
| description | Description | string |

## Example Invocation

```
Sync permission seed data from D:\www\admin-web\web\src\data\permission-seed.ts to internal/service/system_permission.go
```
