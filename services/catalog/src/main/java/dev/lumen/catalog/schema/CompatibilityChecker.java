package dev.lumen.catalog.schema;

import com.fasterxml.jackson.databind.JsonNode;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.Iterator;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;

/**
 * Decides whether a new version of an event schema is backward compatible with
 * the previous one: every event that was valid under the old schema must still
 * be valid under the new one, so data already in the lake keeps its meaning and
 * clients on the old contract keep working.
 *
 * <p>Covers the JSON Schema keywords the registry supports: type, properties,
 * required, additionalProperties, enum, minimum/maximum, minLength/maxLength,
 * pattern, and nested object/array schemas.
 */
public final class CompatibilityChecker {

    private CompatibilityChecker() {}

    /** Returns human-readable breaking changes; empty means compatible. */
    public static List<String> breakingChanges(JsonNode oldSchema, JsonNode newSchema) {
        List<String> out = new ArrayList<>();
        compare("", oldSchema, newSchema, out);
        return out;
    }

    private static void compare(String path, JsonNode oldS, JsonNode newS, List<String> out) {
        String where = path.isEmpty() ? "(root)" : path;

        Set<String> oldTypes = types(oldS);
        Set<String> newTypes = types(newS);
        if (!newTypes.isEmpty()) {
            for (String t : oldTypes.isEmpty() ? Set.of("any") : oldTypes) {
                if (!accepts(newTypes, t)) {
                    out.add(where + ": type changed from " + oldTypes + " to " + newTypes);
                    return; // deeper checks are meaningless once the type differs
                }
            }
        }

        if (oldS.has("enum") || newS.has("enum")) {
            Set<JsonNode> newValues = values(newS.get("enum"));
            if (newS.has("enum")) {
                if (!oldS.has("enum")) {
                    out.add(where + ": enum added (restricts values that were allowed)");
                } else {
                    for (JsonNode v : values(oldS.get("enum"))) {
                        if (!newValues.contains(v)) {
                            out.add(where + ": enum value " + v + " removed");
                        }
                    }
                }
            }
        }

        tightened(where, "minimum", oldS, newS, true, out);
        tightened(where, "exclusiveMinimum", oldS, newS, true, out);
        tightened(where, "minLength", oldS, newS, true, out);
        tightened(where, "minItems", oldS, newS, true, out);
        tightened(where, "maximum", oldS, newS, false, out);
        tightened(where, "exclusiveMaximum", oldS, newS, false, out);
        tightened(where, "maxLength", oldS, newS, false, out);
        tightened(where, "maxItems", oldS, newS, false, out);

        if (newS.has("pattern") && !Objects.equals(oldS.get("pattern"), newS.get("pattern"))) {
            out.add(where + ": pattern " + (oldS.has("pattern") ? "changed" : "added"));
        }

        compareObject(path, oldS, newS, out);

        if (oldS.has("items") && newS.has("items")) {
            compare(path + "[]", oldS.get("items"), newS.get("items"), out);
        } else if (!oldS.has("items") && newS.has("items")) {
            out.add(where + ": items schema added (restricts array contents)");
        }
    }

    private static void compareObject(String path, JsonNode oldS, JsonNode newS, List<String> out) {
        JsonNode oldProps = oldS.path("properties");
        JsonNode newProps = newS.path("properties");
        Set<String> oldRequired = strings(oldS.get("required"));
        Set<String> newRequired = strings(newS.get("required"));
        boolean oldClosed = oldS.path("additionalProperties").isBoolean() && !oldS.path("additionalProperties").asBoolean();
        boolean newClosed = newS.path("additionalProperties").isBoolean() && !newS.path("additionalProperties").asBoolean();

        for (Iterator<Map.Entry<String, JsonNode>> it = oldProps.fields(); it.hasNext(); ) {
            Map.Entry<String, JsonNode> e = it.next();
            String child = path.isEmpty() ? e.getKey() : path + "." + e.getKey();
            if (newProps.has(e.getKey())) {
                compare(child, e.getValue(), newProps.get(e.getKey()), out);
            } else {
                // Even under an open schema the field loses its type and
                // dashboards built on it break, so removal is never allowed.
                out.add(child + ": property removed");
            }
        }

        for (String r : newRequired) {
            if (!oldRequired.contains(r)) {
                String child = path.isEmpty() ? r : path + "." + r;
                out.add(child + ": became required");
            }
        }

        if (newClosed && !oldClosed) {
            out.add((path.isEmpty() ? "(root)" : path) + ": additionalProperties set to false");
        }
    }

    private static void tightened(String where, String kw, JsonNode oldS, JsonNode newS, boolean isLower, List<String> out) {
        if (!newS.has(kw) || !newS.get(kw).isNumber()) {
            return;
        }
        double n = newS.get(kw).asDouble();
        if (!oldS.has(kw)) {
            out.add(where + ": " + kw + " " + newS.get(kw) + " added");
            return;
        }
        double o = oldS.get(kw).asDouble();
        if (isLower ? n > o : n < o) {
            out.add(where + ": " + kw + " tightened from " + oldS.get(kw) + " to " + newS.get(kw));
        }
    }

    private static Set<String> types(JsonNode s) {
        Set<String> out = new HashSet<>();
        JsonNode t = s.get("type");
        if (t == null) {
            return out;
        }
        if (t.isArray()) {
            t.forEach(x -> out.add(x.asText()));
        } else {
            out.add(t.asText());
        }
        return out;
    }

    /** "number" accepts every integer, so integer -> number is a widening. */
    private static boolean accepts(Set<String> newTypes, String oldType) {
        return newTypes.contains(oldType) || ("integer".equals(oldType) && newTypes.contains("number"));
    }

    private static Set<String> strings(JsonNode arr) {
        Set<String> out = new HashSet<>();
        if (arr != null && arr.isArray()) {
            arr.forEach(x -> out.add(x.asText()));
        }
        return out;
    }

    private static Set<JsonNode> values(JsonNode arr) {
        Set<JsonNode> out = new HashSet<>();
        if (arr != null && arr.isArray()) {
            arr.forEach(out::add);
        }
        return out;
    }
}
