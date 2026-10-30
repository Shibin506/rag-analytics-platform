package dev.lumen.catalog.analytics;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.util.ArrayList;
import java.util.List;
import java.util.Properties;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;

/**
 * Lake analytics through Trino. Every query filters on the `dt` partition
 * column first, so Trino reads only the days asked for (see
 * pipelines/trino/README in the repo for the table layout).
 */
@Service
public class TrinoAnalytics {

    private static final Logger log = LoggerFactory.getLogger(TrinoAnalytics.class);

    public record DailyEvents(String day, String eventType, double events, double users) {}

    public record DailyRevenue(String day, String currency, double orders, double revenueCents) {}

    public record PageCount(String path, double views, double users) {}

    public record Overview(boolean available, String latestDay, int queryMs) {}

    @FunctionalInterface
    private interface RowMapper<T> {
        T map(ResultSet rs) throws SQLException;
    }

    private final String url;
    private final String user;
    private final int timeoutSeconds;

    public TrinoAnalytics(@Value("${lumen.trino.url}") String url,
                          @Value("${lumen.trino.user:lumen}") String user,
                          @Value("${lumen.trino.timeout-seconds:20}") int timeoutSeconds) {
        this.url = url;
        this.user = user;
        this.timeoutSeconds = timeoutSeconds;
    }

    public Overview overview(String tenant) {
        long start = System.nanoTime();
        try {
            List<String> latest = query(
                    "SELECT CAST(max(dt) AS varchar) FROM event_daily_rollup WHERE tenant_id = ?",
                    rs -> rs.getString(1), tenant);
            return new Overview(true, latest.isEmpty() ? null : latest.get(0), elapsed(start));
        } catch (SQLException e) {
            log.warn("trino unavailable: {}", e.getMessage());
            return new Overview(false, null, elapsed(start));
        }
    }

    public List<DailyEvents> daily(String tenant, int days) throws SQLException {
        return query("""
                SELECT CAST(dt AS varchar), event_type, events, users
                FROM event_daily_rollup
                WHERE dt >= current_date - INTERVAL '1' DAY * ? AND tenant_id = ?
                ORDER BY dt, event_type""",
                rs -> new DailyEvents(rs.getString(1), rs.getString(2), rs.getDouble(3), rs.getDouble(4)),
                clamp(days, 1, 400), tenant);
    }

    public List<DailyRevenue> revenue(String tenant, int days) throws SQLException {
        return query("""
                SELECT CAST(dt AS varchar), currency, orders, revenue_cents
                FROM revenue_daily
                WHERE dt >= current_date - INTERVAL '1' DAY * ? AND tenant_id = ?
                ORDER BY dt, currency""",
                rs -> new DailyRevenue(rs.getString(1), rs.getString(2), rs.getDouble(3), rs.getDouble(4)),
                clamp(days, 1, 400), tenant);
    }

    public List<PageCount> topPages(String tenant, int days, int limit) throws SQLException {
        return query("""
                SELECT path, count(*) AS views, approx_distinct(user_id) AS users
                FROM events
                WHERE dt >= current_date - INTERVAL '1' DAY * ? AND tenant_id = ? AND event_type = 'page.view'
                GROUP BY path ORDER BY views DESC LIMIT ?""",
                rs -> new PageCount(rs.getString(1), rs.getDouble(2), rs.getDouble(3)),
                clamp(days, 1, 400), tenant, clamp(limit, 1, 50));
    }

    private <T> List<T> query(String sql, RowMapper<T> mapper, Object... params) throws SQLException {
        Properties p = new Properties();
        p.setProperty("user", user);
        try (Connection c = DriverManager.getConnection(url, p);
             PreparedStatement st = c.prepareStatement(sql)) {
            st.setQueryTimeout(timeoutSeconds);
            for (int i = 0; i < params.length; i++) {
                st.setObject(i + 1, params[i]);
            }
            List<T> out = new ArrayList<>();
            try (ResultSet rs = st.executeQuery()) {
                while (rs.next()) {
                    out.add(mapper.map(rs));
                }
            }
            return out;
        }
    }

    private static int clamp(int v, int lo, int hi) {
        return Math.max(lo, Math.min(v, hi));
    }

    private static int elapsed(long start) {
        return (int) ((System.nanoTime() - start) / 1_000_000);
    }
}
