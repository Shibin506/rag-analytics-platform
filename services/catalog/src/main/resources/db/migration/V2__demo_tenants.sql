-- Demo tenants and starter schemas so the stack works out of the box.
-- API keys: acme -> lumen_acme_demo_key, globex -> lumen_globex_demo_key (demo only).

INSERT INTO tenants (id, name, api_key_sha256) VALUES
    ('acme',   'Acme Analytics', encode(sha256('lumen_acme_demo_key'::bytea), 'hex')),
    ('globex', 'Globex Retail',  encode(sha256('lumen_globex_demo_key'::bytea), 'hex'));

INSERT INTO event_schemas (tenant_id, event_type, version, schema)
SELECT t.id, s.event_type, 1, s.schema::jsonb
FROM tenants t
CROSS JOIN (VALUES
    ('page.view', '{
        "type": "object",
        "required": ["path"],
        "properties": {
            "path":        {"type": "string", "minLength": 1},
            "referrer":    {"type": "string"},
            "duration_ms": {"type": "integer", "minimum": 0}
        },
        "additionalProperties": false
    }'),
    ('order.placed', '{
        "type": "object",
        "required": ["order_id", "amount_cents", "currency"],
        "properties": {
            "order_id":     {"type": "string"},
            "amount_cents": {"type": "integer", "minimum": 0},
            "currency":     {"type": "string", "pattern": "^[A-Z]{3}$"},
            "items":        {"type": "integer", "minimum": 1}
        },
        "additionalProperties": false
    }'),
    ('support.ticket', '{
        "type": "object",
        "required": ["ticket_id", "priority"],
        "properties": {
            "ticket_id": {"type": "string"},
            "priority":  {"type": "string", "enum": ["low", "normal", "high", "urgent"]},
            "topic":     {"type": "string"}
        },
        "additionalProperties": false
    }')
) AS s(event_type, schema);
