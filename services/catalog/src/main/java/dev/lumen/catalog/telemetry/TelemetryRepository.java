package dev.lumen.catalog.telemetry;

import java.util.List;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Repository;

/** Operational data written by the gateway and the RAG service. */
@Repository
public class TelemetryRepository {

    public record IngestPoint(String minute, long accepted, long rejected) {}

    public record EventTypeTotal(String eventType, double accepted, double rejected) {}

    public record IngestTotals(double accepted, double rejected, double rejectionRate, List<EventTypeTotal> byEventType) {}

    public record Document(String id, String title, String status, int chunkCount, String error,
                           String createdAt, String indexedAt) {}

    public record AnswerLog(String id, String question, String answer, String mode, int retrievalMs,
                            int totalMs, String createdAt, int upVotes, int downVotes) {}

    public record FeedbackSummary(int answers, int up, int down, Double satisfaction) {}

    private final JdbcTemplate jdbc;

    public TelemetryRepository(JdbcTemplate jdbc) {
        this.jdbc = jdbc;
    }

    public String tenantName(String tenant) {
        return jdbc.queryForObject("SELECT name FROM tenants WHERE id = ?", String.class, tenant);
    }

    /** One point per minute, including empty minutes, so charts have no gaps. */
    public List<IngestPoint> ingestSeries(String tenant, int minutes) {
        int m = Math.max(1, Math.min(minutes, 24 * 60));
        return jdbc.query("""
                SELECT to_char(g.minute AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI"Z"'),
                       coalesce(sum(s.accepted), 0), coalesce(sum(s.rejected), 0)
                FROM generate_series(date_trunc('minute', now()) - make_interval(mins => ? - 1),
                                     date_trunc('minute', now()), interval '1 minute') AS g(minute)
                LEFT JOIN ingest_stats s ON s.minute = g.minute AND s.tenant_id = ?
                GROUP BY g.minute ORDER BY g.minute""",
                (rs, i) -> new IngestPoint(rs.getString(1), rs.getLong(2), rs.getLong(3)), m, tenant);
    }

    public IngestTotals ingestTotals(String tenant, int hours) {
        List<EventTypeTotal> rows = jdbc.query("""
                SELECT event_type, sum(accepted), sum(rejected) FROM ingest_stats
                WHERE tenant_id = ? AND minute > now() - make_interval(hours => ?)
                GROUP BY event_type ORDER BY sum(accepted) + sum(rejected) DESC""",
                (rs, i) -> new EventTypeTotal(rs.getString(1), rs.getDouble(2), rs.getDouble(3)),
                tenant, Math.max(1, Math.min(hours, 24 * 31)));
        double acc = rows.stream().mapToDouble(EventTypeTotal::accepted).sum();
        double rej = rows.stream().mapToDouble(EventTypeTotal::rejected).sum();
        return new IngestTotals(acc, rej, acc + rej == 0 ? 0 : rej / (acc + rej), rows);
    }

    public List<Document> documents(String tenant) {
        return jdbc.query("""
                SELECT id::text, title, status, chunk_count, error, created_at::text, indexed_at::text
                FROM documents WHERE tenant_id = ? ORDER BY created_at DESC LIMIT 200""",
                (rs, i) -> new Document(rs.getString(1), rs.getString(2), rs.getString(3), rs.getInt(4),
                        rs.getString(5), rs.getString(6), rs.getString(7)), tenant);
    }

    public List<AnswerLog> answers(String tenant, int limit) {
        return jdbc.query("""
                SELECT a.id::text, a.question, a.answer, a.mode, a.retrieval_ms, a.total_ms, a.created_at::text,
                       count(*) FILTER (WHERE f.vote = 1), count(*) FILTER (WHERE f.vote = -1)
                FROM answers a LEFT JOIN feedback f ON f.answer_id = a.id
                WHERE a.tenant_id = ?
                GROUP BY a.id ORDER BY a.created_at DESC LIMIT ?""",
                (rs, i) -> new AnswerLog(rs.getString(1), rs.getString(2), rs.getString(3), rs.getString(4),
                        rs.getInt(5), rs.getInt(6), rs.getString(7), rs.getInt(8), rs.getInt(9)),
                tenant, Math.max(1, Math.min(limit, 200)));
    }

    public FeedbackSummary feedback(String tenant) {
        return jdbc.queryForObject("""
                SELECT (SELECT count(*) FROM answers WHERE tenant_id = ?),
                       count(*) FILTER (WHERE f.vote = 1), count(*) FILTER (WHERE f.vote = -1)
                FROM feedback f JOIN answers a ON a.id = f.answer_id WHERE a.tenant_id = ?""",
                (rs, i) -> {
                    int up = rs.getInt(2), down = rs.getInt(3);
                    return new FeedbackSummary(rs.getInt(1), up, down, up + down == 0 ? null : (double) up / (up + down));
                }, tenant, tenant);
    }
}
