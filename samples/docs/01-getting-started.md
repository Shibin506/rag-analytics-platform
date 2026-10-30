# Getting started with Acme Analytics

Acme Analytics collects product events from your applications, stores them in a data lake, and answers questions about them on dashboards and through the assistant.

## Create an API key

Every request is authenticated with a tenant API key sent in the `X-API-Key` header (REST) or the `x-api-key` metadata key (gRPC). Admins create keys under **Settings → API keys**. A key is shown only once; Acme stores only its SHA-256 hash, so a lost key cannot be recovered and must be replaced.

## Rotate a key without downtime

Create the new key first, deploy it to every client, then revoke the old key. Both keys work during the overlap. We recommend rotating keys every 90 days and immediately after an employee with access leaves.

## Send your first event

Post a JSON body with an `events` array to `/v1/events`. Each event needs an `event_id` (a UUID you generate), an `event_type` that has a registered schema, an `occurred_at` timestamp in RFC 3339 format, and a `properties` object. A successful call returns the number of accepted events and a list of rejected ones with a reason for each.

## Where to go next

Register schemas for your own event types, upload help-center documents so the assistant can answer questions about them, and open the Overview dashboard to watch events arrive minute by minute.
