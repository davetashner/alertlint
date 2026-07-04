# alertlint

A skill/CLI that analyzes a service's alert configurations alongside its prior alerting history — and the actions taken (or not taken) for each alert — to score services and recommend concrete improvements.

## What it does

alertlint looks at three inputs for a service:

1. **Alert configurations** — the alerts currently defined for the service (thresholds, conditions, severities, routing).
2. **Alerting history** — when alerts fired, how often, for how long, and at what times.
3. **Action taken** — what responders actually did when each alert fired (acknowledged, remediated, silenced, ignored).

From these it produces:

- **A service score** — a quantitative measure of alerting health per service, comparable across services and trackable over time.
- **Noise-reduction recommendations** — alerts that fire frequently but rarely result in action are candidates for tuning, downgrading, or removal.
- **Missing-alert recommendations** — low-hanging-fruit alerts the service should have but doesn't (e.g., standard golden-signal coverage gaps).
- **Threshold recommendations** — better thresholds for existing alerts, informed by historical firing patterns and responder behavior.

## Status

🚧 **Early scaffolding.** No functional code yet. See [docs/specs](docs/specs/) for what's being designed and [docs/decision-records](docs/decision-records/) for decisions made along the way.

## Repository layout

```
docs/
├── decision-records/   # Architecture Decision Records (ADRs)
└── specs/              # Feature and design specifications
```

## Usage

_TBD — CLI interface and skill packaging are not yet defined._

## Contributing

All changes go through pull requests into `main` (enforced by branch protection). The backlog is managed with [beads](https://github.com/steveyegge/beads) in `.beads/` — run `bd list --status open` to see what's up next.

## License

See [LICENSE](LICENSE).
