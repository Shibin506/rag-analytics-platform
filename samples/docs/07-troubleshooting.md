# Troubleshooting

## 401 Unauthorized

The API key is missing, mistyped or revoked. Check that the header is named `X-API-Key` and that the key has not been rotated.

## 422 Unprocessable Entity

Every event in the batch was rejected. Read the `reason` of each rejection; the most common causes are an `event_type` with no registered schema, a property of the wrong type (for example an amount sent as the string "12.50" instead of an integer number of cents), or a lowercase currency code where the schema requires three capital letters.

## "occurred_at is in the future"

The device clock is more than 5 minutes ahead. Send timestamps from a server clock synchronised with NTP, or include the device time as a property and set `occurred_at` on your server.

## 429 Too Many Requests

You exceeded 1,000 requests per second for your tenant. Batch more events per request (up to 5,000) or switch to gRPC streaming, and retry with exponential backoff starting at 1 second.

## Events missing from a dashboard

Analytical dashboards are rebuilt nightly at 02:00 UTC, so events sent today appear tomorrow. Check the Overview page, which is real time, to confirm the events were accepted.
