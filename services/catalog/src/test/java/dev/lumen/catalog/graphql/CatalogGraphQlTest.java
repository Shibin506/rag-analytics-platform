package dev.lumen.catalog.graphql;

import static org.assertj.core.api.Assertions.assertThat;

import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.condition.EnabledIfEnvironmentVariable;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.web.server.LocalServerPort;
import org.springframework.graphql.execution.ErrorType;
import org.springframework.graphql.test.tester.HttpGraphQlTester;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.test.web.reactive.server.WebTestClient;

/**
 * Runs the whole app against a real Postgres (Flyway migrates an empty test
 * database). Trino points at a closed port, so analytics must degrade to
 * `available: false` rather than failing the request.
 */
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@EnabledIfEnvironmentVariable(named = "TEST_JDBC_URL", matches = ".+")
class CatalogGraphQlTest {

    @DynamicPropertySource
    static void props(DynamicPropertyRegistry r) {
        r.add("spring.datasource.url", () -> System.getenv("TEST_JDBC_URL"));
        r.add("lumen.trino.url", () -> "jdbc:trino://127.0.0.1:1/lake/curated");
        r.add("lumen.trino.timeout-seconds", () -> "2");
    }

    @LocalServerPort
    int port;

    @Autowired
    JdbcTemplate jdbc;

    HttpGraphQlTester as(String key) {
        WebTestClient client = WebTestClient.bindToServer().baseUrl("http://localhost:" + port + "/graphql")
                .defaultHeader("X-API-Key", key).build();
        return HttpGraphQlTester.create(client);
    }

    @BeforeAll
    static void requireEmptyDb() {
        // The test database is created empty by the caller (CI or a developer);
        // Flyway applies V1/V2 on startup.
    }

    @Test
    void rejectsRequestsWithoutAValidKey() {
        as("nope").document("{ viewer { id } }").execute()
                .errors().satisfy(errs -> assertThat(errs).anySatisfy(e ->
                        assertThat(e.getErrorType()).isEqualTo(ErrorType.UNAUTHORIZED)));
    }

    @Test
    void viewerSeesOnlyItsOwnData() {
        jdbc.update("""
                INSERT INTO ingest_stats (tenant_id, minute, event_type, accepted, rejected)
                VALUES ('acme', date_trunc('minute', now()), 'page.view', 90, 10),
                       ('globex', date_trunc('minute', now()), 'page.view', 5, 0)
                ON CONFLICT DO NOTHING""");

        var res = as("lumen_acme_demo_key").document("""
                { viewer { id name
                    eventTypes { name latestVersion }
                    ingestSeries(minutes: 5) { minute accepted rejected }
                    ingestTotals(hours: 1) { accepted rejected rejectionRate }
                    feedback { answers up down satisfaction }
                    analytics { available daily { day } } } }""").execute();

        res.path("viewer.id").entity(String.class).isEqualTo("acme");
        res.path("viewer.name").entity(String.class).isEqualTo("Acme Analytics");
        res.path("viewer.eventTypes[*].name").entityList(String.class)
                .containsExactly("order.placed", "page.view", "support.ticket");
        res.path("viewer.ingestSeries").entityList(Map.class).hasSize(5);
        res.path("viewer.ingestTotals.accepted").entity(Double.class).isEqualTo(90.0);
        res.path("viewer.ingestTotals.rejectionRate").entity(Double.class).isEqualTo(0.1);
        res.path("viewer.analytics.available").entity(Boolean.class).isEqualTo(false);
        res.path("viewer.analytics.daily").entityList(Map.class).hasSize(0);
    }

    @Test
    void registersCompatibleVersionsAndRejectsBreakingOnes() {
        String compatible = """
                {"type":"object","required":["ticket_id","priority"],
                 "properties":{"ticket_id":{"type":"string"},
                   "priority":{"type":"string","enum":["low","normal","high","urgent","critical"]},
                   "topic":{"type":"string"},"channel":{"type":"string"}},
                 "additionalProperties":false}""";
        String breaking = compatible.replace("\"topic\":{\"type\":\"string\"},", "");

        var dry = as("lumen_acme_demo_key")
                .document("query($s: String!) { checkSchema(eventType: \"support.ticket\", schema: $s) { ok version breakingChanges } }")
                .variable("s", compatible).execute();
        dry.path("checkSchema.ok").entity(Boolean.class).isEqualTo(true);
        dry.path("checkSchema.version").entity(Integer.class).isEqualTo(2);
        assertThat(jdbc.queryForObject("SELECT max(version) FROM event_schemas WHERE tenant_id='acme' AND event_type='support.ticket'",
                Integer.class)).as("a dry run writes nothing").isEqualTo(1);

        String register = "mutation($s: String!) { registerSchema(eventType: \"support.ticket\", schema: $s) { ok version errors breakingChanges } }";
        as("lumen_acme_demo_key").document(register).variable("s", compatible).execute()
                .path("registerSchema.version").entity(Integer.class).isEqualTo(2);

        var bad = as("lumen_acme_demo_key").document(register).variable("s", breaking).execute();
        bad.path("registerSchema.ok").entity(Boolean.class).isEqualTo(false);
        bad.path("registerSchema.breakingChanges").entityList(String.class).contains("topic: property removed");

        var again = as("lumen_acme_demo_key").document(register).variable("s", compatible).execute();
        again.path("registerSchema.errors").entityList(String.class).contains("schema is identical to version 2");

        // globex's schemas are untouched
        assertThat(jdbc.queryForObject("SELECT max(version) FROM event_schemas WHERE tenant_id='globex' AND event_type='support.ticket'",
                Integer.class)).isEqualTo(1);
    }

    @Test
    void rejectsInvalidSchemas() {
        var res = as("lumen_acme_demo_key")
                .document("{ checkSchema(eventType: \"Bad Type\", schema: \"[1,2\") { ok errors } }").execute();
        res.path("checkSchema.ok").entity(Boolean.class).isEqualTo(false);
        res.path("checkSchema.errors").entityList(String.class).hasSize(2);

        var notObject = as("lumen_acme_demo_key")
                .document("{ checkSchema(eventType: \"x.y\", schema: \"{\\\"type\\\":\\\"strnig\\\"}\") { errors } }").execute();
        List<String> errors = notObject.path("checkSchema.errors").entityList(String.class).get();
        assertThat(errors).anySatisfy(e -> assertThat(e).contains("top-level type must be"));
        assertThat(errors).anySatisfy(e -> assertThat(e).contains("unknown type \"strnig\""));
    }
}
