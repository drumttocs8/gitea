# SCADA Studio — Gitea Overlay

Thin Docker overlay on vanilla [Gitea](https://gitea.com) for the SCADA Studio platform.

## What this repo does

- Extends the official `gitea/gitea:1.22-rootless` Docker image
- Adds custom templates for the SCADA Studio toolbar and file-action buttons
- Deployed on Railway as a separate service

## Upgrading Gitea

Just update the tag in `Dockerfile`:

```dockerfile
FROM gitea/gitea:1.23-rootless   # ← bump version here
```

Push to trigger a Railway rebuild.

## Custom Templates

| File | Injection Point | Purpose |
|---|---|---|
| `custom/templates/custom/header.tmpl` | `<head>` | CSS for toolbar, buttons, modals |
| `custom/templates/custom/body_outer_pre.tmpl` | Top of `<body>` | SCADA toolbar HTML |
| `custom/templates/custom/footer.tmpl` | Before `</body>` | All JavaScript — search, parse, points list, similar configs |

## Architecture

```
Browser → Gitea (this repo, UI) → templates call sidecar API via JS
           ↕ webhooks
         Sidecar (scada-studio repo, FastAPI) → PostgreSQL/pgvector
```
