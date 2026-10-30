## API Documentation and Downstream Consumers

- **The API code is the source of truth**: the route constants in `api/routes.go`, the handlers in `api/` and `csp/handlers/`, and every type they expose (`api/apicommon`, plus `csp/handlers` and `db` types that reach the wire)
- `docs/swagger.yaml` is generated from the swag annotations - run `make swagger` after any API change; never edit it by hand. If it is wrong, fix the annotation (a `@Router` path must match the handler's route constant)
- Don't add hand-written endpoint docs or request collections to this repo (the old `api/docs.md` and `api/examples.http` were removed after they drifted)
- The integrator guides live in `vocdoni/vocdoni.io` (`content/developers/docs/en/`) and the TypeScript SDK in `vocdoni/vocdoni-integrator-sdk`; both follow the API, so when they disagree with it, they are what needs fixing
- When a change alters anything an API consumer can observe, suggest opening an issue or a PR in `vocdoni/vocdoni.io` (and in `vocdoni/vocdoni-integrator-sdk` when the SDK needs updating or should support the change), and record that follow-up in the PR description
- See the "API source of truth" section of `AGENTS.md` for details, including which vocdoni.io branch to target
