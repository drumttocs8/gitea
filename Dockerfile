FROM gitea/gitea:main-nightly-rootless

# ── SCADA Studio: thin overlay on vanilla Gitea ──────────────
# To upgrade Gitea, just bump the tag above and rebuild.
# Custom templates inject a toolbar + per-file SCADA action buttons.
#
# DB migration v326 requires nightly or 1.24+.
# The rootless image runs as UID 1000, so we preserve ownership.

COPY --chown=1000:1000 custom/templates/ /var/lib/gitea/custom/templates/
