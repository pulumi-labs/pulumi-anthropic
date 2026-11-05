---
title: Anthropic
meta_desc: Use the Anthropic provider for Pulumi to manage Claude Managed Agents, skills, workspaces, and the rest of an Anthropic organization as code.
layout: package
---

The Anthropic provider manages the [Anthropic platform](https://platform.claude.com) as
code: Claude Managed Agents and the environments, vaults, skills, and schedules they run
with, and the organization around them — workspaces, members, service accounts, workload
identity federation, encryption keys, and spend limits.

The provider must be configured with Anthropic credentials; see
[Installation & Configuration](./installation-configuration/).

## Example

An agent with a custom skill, run hourly by two deployments: a canary that follows the
agent's latest version, and a stable deployment pinned to a version until it is promoted.

{{< chooser language "typescript,python,yaml" >}}

{{% choosable language typescript %}}

```typescript
import * as pulumi from "@pulumi/pulumi";
import * as anthropic from "@pulumi-labs/anthropic";

const config = new pulumi.Config();

const skill = new anthropic.skills.Skill("triage", {
    source: new pulumi.asset.FileArchive("./skills/triage"),
});

const environment = new anthropic.agents.Environment("sandbox", {
    name: "triage-sandbox",
    config: { type: "cloud", networking: { type: "limited", allowedHosts: ["api.github.com"] } },
});

const agent = new anthropic.agents.Agent("triage", {
    name: "issue-triage",
    model: { id: "claude-opus-5-5" },
    system: "You triage GitHub issues.",
    tools: [{ type: "agent_toolset_20260401" }],
    skills: [{ type: "custom", skillId: skill.id, version: skill.latestVersionId }],
});

const common = {
    environmentId: environment.id,
    initialEvents: [{ type: "user.message", content: [{ type: "text", text: "Triage new issues." }] }],
    schedule: { expression: "0 * * * *", timezone: "UTC" },
};

new anthropic.agents.Deployment("canary", {
    ...common,
    name: "triage-canary",
    agent: { id: agent.id, version: agent.version },
});

new anthropic.agents.Deployment("stable", {
    ...common,
    name: "triage-stable",
    agent: { id: agent.id, version: config.requireNumber("stableVersion") },
});
```

{{% /choosable %}}

{{% choosable language python %}}

```python
import pulumi
import pulumi_labs_anthropic as anthropic

config = pulumi.Config()

skill = anthropic.skills.Skill("triage", source=pulumi.FileArchive("./skills/triage"))

environment = anthropic.agents.Environment(
    "sandbox",
    name="triage-sandbox",
    config={"type": "cloud", "networking": {"type": "limited", "allowed_hosts": ["api.github.com"]}},
)

agent = anthropic.agents.Agent(
    "triage",
    name="issue-triage",
    model={"id": "claude-opus-5-5"},
    system="You triage GitHub issues.",
    tools=[{"type": "agent_toolset_20260401"}],
    skills=[{"type": "custom", "skill_id": skill.id, "version": skill.latest_version_id}],
)

common = dict(
    environment_id=environment.id,
    initial_events=[{"type": "user.message", "content": [{"type": "text", "text": "Triage new issues."}]}],
    schedule={"expression": "0 * * * *", "timezone": "UTC"},
)

anthropic.agents.Deployment("canary", name="triage-canary",
                            agent={"id": agent.id, "version": agent.version}, **common)
anthropic.agents.Deployment("stable", name="triage-stable",
                            agent={"id": agent.id, "version": config.require_int("stableVersion")}, **common)
```

{{% /choosable %}}

{{% choosable language yaml %}}

```yaml
config:
  stableVersion:
    type: integer

resources:
  skill:
    type: anthropic:skills:Skill
    properties:
      source:
        fn::fileArchive: ./skills/triage
  environment:
    type: anthropic:agents:Environment
    properties:
      name: triage-sandbox
      config:
        type: cloud
        networking:
          type: limited
          allowedHosts: [api.github.com]
  agent:
    type: anthropic:agents:Agent
    properties:
      name: issue-triage
      model:
        id: claude-opus-5-5
      system: You triage GitHub issues.
      tools:
        - type: agent_toolset_20260401
      skills:
        - type: custom
          skillId: ${skill.id}
          version: ${skill.latestVersionId}
  canary:
    type: anthropic:agents:Deployment
    properties:
      name: triage-canary
      agent:
        id: ${agent.id}
        version: ${agent.version}
      environmentId: ${environment.id}
      initialEvents:
        - type: user.message
          content:
            - type: text
              text: Triage new issues.
      schedule:
        expression: "0 * * * *"
        timezone: UTC
  stable:
    type: anthropic:agents:Deployment
    properties:
      name: triage-stable
      agent:
        id: ${agent.id}
        version: ${stableVersion}
      environmentId: ${environment.id}
      initialEvents:
        - type: user.message
          content:
            - type: text
              text: Triage new issues.
      schedule:
        expression: "0 * * * *"
        timezone: UTC
```

{{% /choosable %}}

{{< /chooser >}}
