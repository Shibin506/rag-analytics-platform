# Schema registry

Every event type has a JSON Schema that describes its `properties`. The ingest gateway validates each event against the latest version of its schema and rejects events that do not match, naming every field that failed.

## Registering a new version

Register a new version from the Schemas page or with the `registerSchema` GraphQL mutation. New versions are enforced by every gateway within 15 seconds; there is no deploy.

## Compatibility rules

New versions must be backward compatible, so dashboards and queries built on older data keep working. A new version may:

- add optional properties,
- make a required property optional,
- relax a constraint (for example raise a `maxLength`).

A new version may not remove a property, change a property's type, add a new required property, or narrow an `enum`. Incompatible versions are rejected with a list of the breaking changes. To make a breaking change, register a new event type such as `order.placed.v2` and migrate clients to it.

## Versions in the lake

Every stored event records the `schema_version` it was validated against, so analysts can tell which rules applied to old data.
