FROM gitea/gitea:1.22-rootless

# ── SCADA Studio: thin overlay on vanilla Gitea ──────────────
# To upgrade Gitea, just bump the tag above and rebuild.
# Custom templates inject a toolbar + per-file SCADA action buttons.

COPY custom/templates/ /var/lib/gitea/custom/templates/
