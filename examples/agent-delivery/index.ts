import * as pulumi from "@pulumi/pulumi";
import * as anthropic from "@pulumi/anthropic";

const config = new pulumi.Config();

// A custom skill, uploaded from a local directory. Editing SKILL.md publishes a new
// skill version on the next `pulumi up`.
const triage = new anthropic.skills.Skill("triage", {
    source: new pulumi.asset.FileArchive("./skills/triage"),
});

// The sandbox sessions run in: network access limited to GitHub.
const environment = new anthropic.agents.Environment("sandbox", {
    name: "triage-sandbox",
    config: {
        type: "cloud",
        networking: {
            type: "limited",
            allowedHosts: ["github.com", "api.github.com"],
            allowMcpServers: true,
        },
    },
});

// GitHub credentials for the MCP server, kept in a vault rather than in the agent.
const vault = new anthropic.agents.Vault("github", { displayName: "GitHub" });
new anthropic.agents.VaultCredential("github-token", {
    vaultId: vault.id,
    displayName: "GitHub MCP token",
    auth: {
        type: "static_bearer",
        mcpServerUrl: "https://api.githubcopilot.com/mcp/",
        token: config.requireSecret("githubToken"),
    },
});

// The agent. Every change to its configuration creates a new version.
const agent = new anthropic.agents.Agent("triage", {
    name: "issue-triage",
    model: { id: "claude-opus-5-5" },
    system: "You triage GitHub issues for the pulumi/pulumi repository.",
    mcpServers: [{ name: "github", url: "https://api.githubcopilot.com/mcp/" }],
    tools: [
        { type: "agent_toolset_20260401", defaultConfig: { permissionPolicy: "always_allow" } },
        { type: "mcp_toolset", mcpServerName: "github" },
    ],
    skills: [{ type: "custom", skillId: triage.id, version: triage.latestVersionId }],
});

const deploymentArgs = {
    environmentId: environment.id,
    vaultIds: [vault.id],
    initialEvents: [{
        type: "user.message",
        content: [{ type: "text", text: "Triage the issues opened in the last hour." }],
    }],
    schedule: { expression: "0 * * * *", timezone: "UTC" },
    paused: config.getBoolean("paused") ?? false,
};

// The canary always runs the newest agent version.
new anthropic.agents.Deployment("canary", {
    ...deploymentArgs,
    name: "triage-canary",
    agent: { id: agent.id, version: agent.version },
});

// Stable stays on a pinned version until it is promoted by changing `stableVersion`.
new anthropic.agents.Deployment("stable", {
    ...deploymentArgs,
    name: "triage-stable",
    agent: { id: agent.id, version: config.getNumber("stableVersion") ?? agent.version },
});

export const agentId = agent.id;
export const agentVersion = agent.version;
