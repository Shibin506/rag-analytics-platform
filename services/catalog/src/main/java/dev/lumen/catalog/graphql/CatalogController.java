package dev.lumen.catalog.graphql;

import dev.lumen.catalog.analytics.TrinoAnalytics;
import dev.lumen.catalog.auth.ApiKeyInterceptor;
import dev.lumen.catalog.schema.SchemaRegistry;
import dev.lumen.catalog.telemetry.TelemetryRepository;
import graphql.GraphQLContext;
import java.sql.SQLException;
import java.util.List;
import org.springframework.graphql.data.method.annotation.Argument;
import org.springframework.graphql.data.method.annotation.MutationMapping;
import org.springframework.graphql.data.method.annotation.QueryMapping;
import org.springframework.graphql.data.method.annotation.SchemaMapping;
import org.springframework.graphql.execution.ErrorType;
import org.springframework.graphql.execution.DataFetcherExceptionResolverAdapter;
import org.springframework.stereotype.Controller;
import org.springframework.stereotype.Component;
import graphql.GraphQLError;
import graphql.GraphqlErrorBuilder;
import graphql.schema.DataFetchingEnvironment;

@Controller
public class CatalogController {

    public record Tenant(String id) {}

    public record Analytics(String tenantId, boolean available, String latestDay, int queryMs) {}

    public record EventType(String tenantId, String name, int latestVersion) {}

    /** Thrown when a request has no valid API key. */
    public static class UnauthorizedException extends RuntimeException {
        public UnauthorizedException() {
            super("missing or invalid X-API-Key");
        }
    }

    private final SchemaRegistry registry;
    private final TelemetryRepository telemetry;
    private final TrinoAnalytics trino;

    public CatalogController(SchemaRegistry registry, TelemetryRepository telemetry, TrinoAnalytics trino) {
        this.registry = registry;
        this.telemetry = telemetry;
        this.trino = trino;
    }

    static String tenant(GraphQLContext ctx) {
        String t = ctx.get(ApiKeyInterceptor.TENANT);
        if (t == null) {
            throw new UnauthorizedException();
        }
        return t;
    }

    @QueryMapping
    public Tenant viewer(GraphQLContext ctx) {
        return new Tenant(tenant(ctx));
    }

    @QueryMapping
    public SchemaRegistry.Check checkSchema(@Argument String eventType, @Argument String schema, GraphQLContext ctx) {
        return registry.check(tenant(ctx), eventType, schema);
    }

    @MutationMapping
    public SchemaRegistry.Check registerSchema(@Argument String eventType, @Argument String schema, GraphQLContext ctx) {
        return registry.register(tenant(ctx), eventType, schema);
    }

    @SchemaMapping
    public String name(Tenant t) {
        return telemetry.tenantName(t.id());
    }

    @SchemaMapping
    public List<EventType> eventTypes(Tenant t) {
        return registry.eventTypes(t.id()).stream()
                .map(e -> new EventType(t.id(), e.name(), e.latestVersion())).toList();
    }

    @SchemaMapping
    public List<SchemaRegistry.SchemaVersion> versions(EventType e) {
        return registry.versions(e.tenantId(), e.name());
    }

    @SchemaMapping
    public List<TelemetryRepository.IngestPoint> ingestSeries(Tenant t, @Argument int minutes) {
        return telemetry.ingestSeries(t.id(), minutes);
    }

    @SchemaMapping
    public TelemetryRepository.IngestTotals ingestTotals(Tenant t, @Argument int hours) {
        return telemetry.ingestTotals(t.id(), hours);
    }

    @SchemaMapping
    public List<TelemetryRepository.Document> documents(Tenant t) {
        return telemetry.documents(t.id());
    }

    @SchemaMapping
    public List<TelemetryRepository.AnswerLog> answers(Tenant t, @Argument int limit) {
        return telemetry.answers(t.id(), limit);
    }

    @SchemaMapping
    public TelemetryRepository.FeedbackSummary feedback(Tenant t) {
        return telemetry.feedback(t.id());
    }

    @SchemaMapping
    public Analytics analytics(Tenant t) {
        TrinoAnalytics.Overview o = trino.overview(t.id());
        return new Analytics(t.id(), o.available(), o.latestDay(), o.queryMs());
    }

    @SchemaMapping
    public List<TrinoAnalytics.DailyEvents> daily(Analytics a, @Argument int days) throws SQLException {
        return a.available() ? trino.daily(a.tenantId(), days) : List.of();
    }

    @SchemaMapping
    public List<TrinoAnalytics.DailyRevenue> revenue(Analytics a, @Argument int days) throws SQLException {
        return a.available() ? trino.revenue(a.tenantId(), days) : List.of();
    }

    @SchemaMapping
    public List<TrinoAnalytics.PageCount> topPages(Analytics a, @Argument int days, @Argument int limit) throws SQLException {
        return a.available() ? trino.topPages(a.tenantId(), days, limit) : List.of();
    }

    /** Maps exceptions to GraphQL errors with a useful classification. */
    @Component
    static class Errors extends DataFetcherExceptionResolverAdapter {
        @Override
        protected GraphQLError resolveToSingleError(Throwable ex, DataFetchingEnvironment env) {
            if (ex instanceof UnauthorizedException) {
                return GraphqlErrorBuilder.newError(env).errorType(ErrorType.UNAUTHORIZED).message(ex.getMessage()).build();
            }
            if (ex instanceof SQLException) {
                return GraphqlErrorBuilder.newError(env).errorType(ErrorType.INTERNAL_ERROR)
                        .message("analytics query failed: " + ex.getMessage()).build();
            }
            return null;
        }
    }
}
