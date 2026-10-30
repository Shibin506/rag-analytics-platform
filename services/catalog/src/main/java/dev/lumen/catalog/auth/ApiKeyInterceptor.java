package dev.lumen.catalog.auth;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.time.Duration;
import java.time.Instant;
import java.util.HexFormat;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import org.springframework.graphql.server.WebGraphQlInterceptor;
import org.springframework.graphql.server.WebGraphQlRequest;
import org.springframework.graphql.server.WebGraphQlResponse;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Component;
import reactor.core.publisher.Mono;

/**
 * Resolves the X-API-Key header to a tenant and puts it in the GraphQL context
 * under {@link #TENANT}. Requests without a valid key get no tenant, and the
 * resolvers reject them.
 */
@Component
public class ApiKeyInterceptor implements WebGraphQlInterceptor {

    public static final String TENANT = "tenantId";
    private static final Duration TTL = Duration.ofMinutes(1);

    private final JdbcTemplate jdbc;
    private final Map<String, Cached> cache = new ConcurrentHashMap<>();

    private record Cached(String tenant, Instant expires) {}

    public ApiKeyInterceptor(JdbcTemplate jdbc) {
        this.jdbc = jdbc;
    }

    @Override
    public Mono<WebGraphQlResponse> intercept(WebGraphQlRequest request, Chain chain) {
        String key = request.getHeaders().getFirst("X-API-Key");
        String tenant = key == null || key.isBlank() ? null : resolve(key);
        if (tenant != null) {
            request.configureExecutionInput((input, builder) ->
                    builder.graphQLContext(Map.of(TENANT, tenant)).build());
        }
        return chain.next(request);
    }

    String resolve(String key) {
        String hash = sha256(key);
        Cached c = cache.get(hash);
        if (c != null && c.expires().isAfter(Instant.now())) {
            return c.tenant();
        }
        List<String> rows = jdbc.queryForList("SELECT id FROM tenants WHERE api_key_sha256 = ?", String.class, hash);
        if (rows.isEmpty()) {
            return null;
        }
        cache.put(hash, new Cached(rows.get(0), Instant.now().plus(TTL)));
        return rows.get(0);
    }

    static String sha256(String s) {
        try {
            byte[] d = MessageDigest.getInstance("SHA-256").digest(s.getBytes(StandardCharsets.UTF_8));
            return HexFormat.of().formatHex(d);
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }
}
