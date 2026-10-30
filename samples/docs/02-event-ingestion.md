# Event ingestion API

Acme accepts events over REST (`POST /v1/events`) and gRPC (`SendEvents` for batches and `StreamEvents` for long-running streams). Both transports run the same validation.

## Batch limits

A single request may contain at most 5,000 events and 32 MB. Larger batches are refused with HTTP 413 (REST) or `INVALID_ARGUMENT` (gRPC). High-volume clients should use `StreamEvents`, which validates events in chunks of 500 as they arrive and never needs to hold the whole stream in memory.

## Partial acceptance

Events are validated one at a time. An invalid event never blocks the rest of the batch: valid events are stored and each invalid one is listed in `rejected` with its index in the request and the reason. If every event in a REST batch is invalid the status is 422 Unprocessable Entity; otherwise it is 200.

## Retries and duplicates

If storage is temporarily unavailable the whole call fails with 503 so your client can retry. Retrying is safe: the nightly compaction job removes duplicate events with the same `event_id`, keeping the copy that was ingested first.

## Timestamps

`occurred_at` must be an RFC 3339 timestamp. Events may be up to 5 minutes in the future to allow for client clock skew and up to 400 days in the past, so you can backfill history. Events are filed under the UTC day on which they occurred, not the day they were sent.
