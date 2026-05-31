# Plugin System Phase 1

## Package Format

A plugin is a zip file. After extraction, the root must contain `plugin.yaml`.

```text
example.plugin.zip
  plugin.yaml
  README.md
  bin/
    windows-amd64/
      plugin.exe
    linux-amd64/
      plugin
  assets/
  migrations/
```

## plugin.yaml

```yaml
id: admin.example-plugin
name: Example Plugin
version: 1.0.0
author: example
description: Example plugin

engine:
  type: process
  protocol: http
  entry:
    windows-amd64: bin/windows-amd64/plugin.exe
    linux-amd64: bin/linux-amd64/plugin

compatibility:
  app: ">=1.0.0 <2.0.0"
  api: "v1"

permissions:
  - route:register
  - menu:register
  - config:read

extension_points:
  - admin.routes
  - admin.menus
  - admin.permissions
```

## Installation Directory

Installed plugins are extracted under:

```text
./data/plugins/{plugin_id}/{version}
```

All extraction and delete operations verify that the target path remains inside `./data/plugins`.

## Admin API

All endpoints require the existing admin JWT, domain middleware, blacklist middleware, and permission middleware.

```text
GET    /api/v1/admin/auth/plugins
GET    /api/v1/admin/auth/plugins/:plugin_id
POST   /api/v1/admin/auth/plugins/install
POST   /api/v1/admin/auth/plugins/:plugin_id/enable
POST   /api/v1/admin/auth/plugins/:plugin_id/disable
DELETE /api/v1/admin/auth/plugins/:plugin_id
```

Install request:

```json
{
  "package_path": "D:/tmp/admin.example-plugin.zip"
}
```

## Phase 1 Limits

Phase 1 does not include an online marketplace, payments, ratings, developer accounts, or remote auditing. Plugins use an external process runtime skeleton; HTTP/gRPC/stdio protocols are reserved in the manifest, but real protocol handshakes are implemented in later phases.

## Future Expansion

Future phases can connect `admin.routes`, `admin.menus`, `admin.permissions`, `cron.tasks`, and `message.handlers` to the real router, menu, permission, cron, and message systems. A marketplace can later reuse the same manifest and installer validation while adding signature verification and review workflows.
