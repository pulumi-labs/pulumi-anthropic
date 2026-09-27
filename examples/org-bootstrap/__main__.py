import pulumi
import pulumi_anthropic as anthropic

config = pulumi.Config()
team = config.get("team") or "research"

# A workspace per team, with its inference kept in the US.
workspace = anthropic.organization.Workspace(
    "workspace",
    name=team,
    display_color="#6C5BB9",
    tags={"team": team, "managed-by": "pulumi"},
    data_residency={"allowed_inference_geos": ["us"], "default_inference_geo": "us"},
)

# A monthly spend limit for the workspace, in cents. Managing spend limits through the
# API is an early-access feature that must be enabled for the organization, so this is
# opt-in: set monthlyLimitCents to use it.
monthly_limit = config.get("monthlyLimitCents")
if monthly_limit:
    anthropic.organization.SpendLimit(
        "monthly-limit",
        scope={"type": "workspace", "workspace_id": workspace.id},
        period="monthly",
        amount=monthly_limit,
    )

# A service account for the team's CI, able to develop in the workspace.
ci = anthropic.identity.ServiceAccount(
    "ci",
    name=f"{team}-ci",
    description=f"CI automation for {team}.",
)
anthropic.identity.ServiceAccountWorkspace(
    "ci-access",
    workspace_id=workspace.id,
    service_account_id=ci.id,
    workspace_role="workspace_developer",
)

# Existing organization members, by user ID, with their workspace roles.
for user_id, role in (config.get_object("members") or {}).items():
    anthropic.organization.WorkspaceMember(
        f"member-{user_id}",
        workspace_id=workspace.id,
        user_id=user_id,
        workspace_role=role,
    )

org = anthropic.get_organization()
pulumi.export("organization", org.name)
pulumi.export("workspaceId", workspace.id)
pulumi.export("serviceAccountId", ci.id)
