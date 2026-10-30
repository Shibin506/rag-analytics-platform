# Data retention and privacy

## Retention

Raw events are kept in the data lake for 400 days. Curated Parquet tables and daily rollups are kept for the life of the account. Uploaded documents are kept until you delete them.

## Deleting a user's data

To honour a GDPR or CCPA deletion request, submit the `user_id` on the Privacy page. Matching events are removed from curated tables at the next nightly compaction and from raw storage within 30 days. You receive an email confirmation when the deletion is complete.

## What we store

We store exactly the properties you send. Do not send passwords, payment card numbers or government ID numbers in event properties; the schema registry can reject such fields if you mark them with `"additionalProperties": false`.

## Encryption

Data is encrypted in transit with TLS 1.2 or higher and at rest with AES-256. API keys are stored only as SHA-256 hashes.

## Data location

Customers on Enterprise can choose to keep all data in the EU (Frankfurt) or the US (Oregon) region.
