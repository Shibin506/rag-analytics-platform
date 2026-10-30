package dev.lumen.catalog.schema;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.ArrayList;
import java.util.List;
import java.util.regex.Pattern;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

@Service
public class SchemaRegistry {

    private static final Pattern EVENT_TYPE = Pattern.compile("^[a-z][a-z0-9_.]{1,63}$");
    private static final List<String> SUPPORTED_TYPES =
            List.of("object", "array", "string", "integer", "number", "boolean", "null");

    public record SchemaVersion(int version, String schema, String createdAt) {}

    public record EventType(String name, int latestVersion) {}

    public record Check(boolean ok, Integer version, List<String> errors, List<String> breakingChanges) {}

    private final JdbcTemplate jdbc;
    private final ObjectMapper json;

    public SchemaRegistry(JdbcTemplate jdbc, ObjectMapper json) {
        this.jdbc = jdbc;
        this.json = json;
    }

    public List<EventType> eventTypes(String tenant) {
        return jdbc.query(
                "SELECT event_type, max(version) FROM event_schemas WHERE tenant_id = ? GROUP BY event_type ORDER BY event_type",
                (rs, i) -> new EventType(rs.getString(1), rs.getInt(2)), tenant);
    }

    public List<SchemaVersion> versions(String tenant, String eventType) {
        return jdbc.query(
                "SELECT version, schema::text, created_at FROM event_schemas WHERE tenant_id = ? AND event_type = ? ORDER BY version DESC",
                (rs, i) -> new SchemaVersion(rs.getInt(1), rs.getString(2), rs.getObject(3).toString()),
                tenant, eventType);
    }

    /** Validates and compares against the latest version without writing anything. */
    public Check check(String tenant, String eventType, String schemaText) {
        List<String> errors = new ArrayList<>();
        JsonNode schema = parse(eventType, schemaText, errors);
        if (!errors.isEmpty()) {
            return new Check(false, null, errors, List.of());
        }
        List<SchemaVersion> existing = versions(tenant, eventType);
        if (existing.isEmpty()) {
            return new Check(true, 1, List.of(), List.of());
        }
        JsonNode latest = read(existing.get(0).schema());
        List<String> breaking = CompatibilityChecker.breakingChanges(latest, schema);
        if (breaking.isEmpty() && latest.equals(schema)) {
            errors.add("schema is identical to version " + existing.get(0).version());
        }
        return new Check(breaking.isEmpty() && errors.isEmpty(), existing.get(0).version() + 1, errors, breaking);
    }

    /**
     * Registers the next version. A transaction-scoped advisory lock on
     * (tenant, event type) serialises concurrent registrations, so two callers
     * can never both get the same version number.
     */
    @Transactional
    public Check register(String tenant, String eventType, String schemaText) {
        jdbc.queryForObject("SELECT pg_advisory_xact_lock(hashtext(?))", Object.class, tenant + "/" + eventType);
        Check c = check(tenant, eventType, schemaText);
        if (!c.ok()) {
            return c;
        }
        jdbc.update("INSERT INTO event_schemas (tenant_id, event_type, version, schema) VALUES (?, ?, ?, ?::jsonb)",
                tenant, eventType, c.version(), schemaText);
        return c;
    }

    private JsonNode parse(String eventType, String text, List<String> errors) {
        if (!EVENT_TYPE.matcher(eventType).matches()) {
            errors.add("event type must match " + EVENT_TYPE.pattern());
        }
        JsonNode schema;
        try {
            schema = json.readTree(text);
        } catch (JsonProcessingException e) {
            errors.add("schema is not valid JSON: " + e.getOriginalMessage());
            return null;
        }
        if (schema == null || !schema.isObject()) {
            errors.add("schema must be a JSON object");
            return null;
        }
        if (!"object".equals(schema.path("type").asText())) {
            errors.add("top-level type must be \"object\" (it describes the event's properties)");
        }
        validateTypes("", schema, errors);
        return schema;
    }

    private void validateTypes(String path, JsonNode node, List<String> errors) {
        JsonNode t = node.get("type");
        if (t != null) {
            for (JsonNode x : t.isArray() ? t : json.createArrayNode().add(t)) {
                if (!SUPPORTED_TYPES.contains(x.asText())) {
                    errors.add((path.isEmpty() ? "(root)" : path) + ": unknown type " + x);
                }
            }
        }
        node.path("properties").fields().forEachRemaining(e ->
                validateTypes(path.isEmpty() ? e.getKey() : path + "." + e.getKey(), e.getValue(), errors));
        if (node.has("items")) {
            validateTypes(path + "[]", node.get("items"), errors);
        }
    }

    private JsonNode read(String text) {
        try {
            return json.readTree(text);
        } catch (JsonProcessingException e) {
            throw new IllegalStateException("stored schema is not JSON", e);
        }
    }
}
