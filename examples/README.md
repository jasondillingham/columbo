# Examples

Example `target.yaml` profiles for known targets. Lift from these when
writing a new target profile.

Placeholder. Will populate as Columbo audits real targets. First two
candidates are Leonard and bosun, since their bughunt-12 and bughunt-1
rounds informed the design and the threat models are already documented
in their respective `audits/` directories.

Planned layout once populated:

```
examples/
├── leonard.target.yaml      # MCP stdio + hook binary + SQLite store
├── bosun.target.yaml        # MCP unix socket + HTTP serve + spawn lifecycle
└── columbo.target.yaml      # self-audit reference profile
```
