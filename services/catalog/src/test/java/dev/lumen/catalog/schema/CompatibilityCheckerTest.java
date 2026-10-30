package dev.lumen.catalog.schema;

import static org.assertj.core.api.Assertions.assertThat;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;

class CompatibilityCheckerTest {

    private static final ObjectMapper M = new ObjectMapper();

    private static final String ORDER_V1 = """
            {"type":"object","required":["order_id","amount_cents","currency"],
             "properties":{
               "order_id":{"type":"string"},
               "amount_cents":{"type":"integer","minimum":0},
               "currency":{"type":"string","pattern":"^[A-Z]{3}$"},
               "channel":{"type":"string","enum":["web","ios","android"]},
               "note":{"type":"string","maxLength":200},
               "shipping":{"type":"object","properties":{"country":{"type":"string"}}}
             },
             "additionalProperties":false}""";

    private static JsonNode json(String s) {
        try {
            return M.readTree(s);
        } catch (Exception e) {
            throw new IllegalArgumentException(e);
        }
    }

    /** ORDER_V1 with one JSON-pointer-ish edit applied via string replacement. */
    private static List<String> changes(String from, String to) {
        String next = ORDER_V1.replace(from, to);
        assertThat(next).as("edit must apply").isNotEqualTo(ORDER_V1);
        return CompatibilityChecker.breakingChanges(json(ORDER_V1), json(next));
    }

    @Test
    void identicalSchemaIsCompatible() {
        assertThat(CompatibilityChecker.breakingChanges(json(ORDER_V1), json(ORDER_V1))).isEmpty();
    }

    @ParameterizedTest(name = "allowed: {0}")
    @CsvSource(delimiter = '|', value = {
            "add optional property      | \"note\":{ | \"gift\":{\"type\":\"boolean\"},\"note\":{",
            "drop a required field      | \"required\":[\"order_id\",\"amount_cents\",\"currency\"] | \"required\":[\"order_id\",\"amount_cents\"]",
            "widen integer to number    | \"amount_cents\":{\"type\":\"integer\" | \"amount_cents\":{\"type\":\"number\"",
            "add enum value             | [\"web\",\"ios\",\"android\"] | [\"web\",\"ios\",\"android\",\"pos\"]",
            "relax maxLength            | \"maxLength\":200 | \"maxLength\":500",
            "relax minimum              | \"minimum\":0 | \"minimum\":-100",
            "remove a constraint        | ,\"maxLength\":200 | ''",
            "open the object            | \"additionalProperties\":false | \"additionalProperties\":true",
    })
    void compatibleChanges(String name, String from, String to) {
        assertThat(changes(from, to.equals("''") ? "" : to)).isEmpty();
    }

    @ParameterizedTest(name = "breaking: {0}")
    @CsvSource(delimiter = '|', value = {
            "remove property         | \"note\":{\"type\":\"string\",\"maxLength\":200}, | ''                                    | note: property removed",
            "change type             | \"order_id\":{\"type\":\"string\"} | \"order_id\":{\"type\":\"integer\"}  | order_id: type changed",
            "narrow number to int    | \"amount_cents\":{\"type\":\"integer\" | \"amount_cents\":{\"type\":\"string\" | amount_cents: type changed",
            "new required field      | \"required\":[\"order_id\", | \"required\":[\"note\",\"order_id\",     | note: became required",
            "remove enum value       | [\"web\",\"ios\",\"android\"] | [\"web\",\"ios\"]                     | channel: enum value \"android\" removed",
            "tighten maxLength       | \"maxLength\":200 | \"maxLength\":100                                     | note: maxLength tightened from 200 to 100",
            "tighten minimum         | \"minimum\":0 | \"minimum\":1                                             | amount_cents: minimum tightened",
            "change pattern          | ^[A-Z]{3}$ | ^[A-Z]{2}$                                                   | currency: pattern changed",
            "nested type change      | \"country\":{\"type\":\"string\"} | \"country\":{\"type\":\"integer\"}  | shipping.country: type changed",
            "nested property removed | {\"country\":{\"type\":\"string\"}} | {}                                 | shipping.country: property removed",
    })
    void breakingChanges(String name, String from, String to, String expected) {
        assertThat(changes(from, to.equals("''") ? "" : to)).anySatisfy(c -> assertThat(c).startsWith(expected));
    }

    @Test
    void closingAnOpenObjectIsBreaking() {
        JsonNode open = json("{\"type\":\"object\",\"properties\":{\"a\":{\"type\":\"string\"}}}");
        JsonNode closed = json("{\"type\":\"object\",\"properties\":{\"a\":{\"type\":\"string\"}},\"additionalProperties\":false}");
        assertThat(CompatibilityChecker.breakingChanges(open, closed))
                .containsExactly("(root): additionalProperties set to false");
    }

    @Test
    void addingEnumOrPatternToAnUnconstrainedFieldIsBreaking() {
        JsonNode v1 = json("{\"type\":\"object\",\"properties\":{\"p\":{\"type\":\"string\"}}}");
        JsonNode v2 = json("{\"type\":\"object\",\"properties\":{\"p\":{\"type\":\"string\",\"enum\":[\"a\"],\"pattern\":\"^a$\"}}}");
        assertThat(CompatibilityChecker.breakingChanges(v1, v2)).hasSize(2);
    }

    @Test
    void arrayItemsAreCompared() {
        JsonNode v1 = json("{\"type\":\"object\",\"properties\":{\"tags\":{\"type\":\"array\",\"items\":{\"type\":\"string\"}}}}");
        JsonNode v2 = json("{\"type\":\"object\",\"properties\":{\"tags\":{\"type\":\"array\",\"items\":{\"type\":\"integer\"}}}}");
        assertThat(CompatibilityChecker.breakingChanges(v1, v2)).containsExactly("tags[]: type changed from [string] to [integer]");
    }
}
