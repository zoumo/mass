package core

import (
	"context"
	"time"
)

type mcpMetricStats struct {
	Count      int64 `json:"count"`
	Failed     int64 `json:"failed"`
	Replayed   int64 `json:"replayed"`
	Changed    int64 `json:"changed"`
	DurationNS int64 `json:"durationNs"`
	P95NS      int64 `json:"p95Ns"`
}

type mcpToolMetrics struct {
	Role string `json:"role"`
	Tool string `json:"tool"`
	mcpMetricStats
}

type mcpMetricBucket struct {
	At         time.Time `json:"at"`
	Role       string    `json:"role"`
	Count      int64     `json:"count"`
	Failed     int64     `json:"failed"`
	DurationNS int64     `json:"durationNs"`
}

type mcpMetrics struct {
	From          time.Time         `json:"from"`
	Until         time.Time         `json:"until"`
	BucketSeconds int64             `json:"bucketSeconds"`
	Total         mcpMetricStats    `json:"total"`
	Tools         []mcpToolMetrics  `json:"tools"`
	Trend         []mcpMetricBucket `json:"trend"`
}

func (s *Store) mcpToolMetrics(ctx context.Context, where string, args []any, from, until time.Time) (mcpMetrics, error) {
	const buckets = 24
	step := int64(until.Sub(from).Seconds()) / buckets
	result := mcpMetrics{From: from, Until: until, BucketSeconds: step, Tools: []mcpToolMetrics{}, Trend: []mcpMetricBucket{}}
	// Order each population independently. The nearest-rank P95 is at
	// count - floor(count/20), including failures and idempotent retries.
	rows, err := s.db.QueryContext(ctx, `WITH filtered AS MATERIALIZED (
 SELECT caller_role,tool,outcome,duration_ns,replayed,changed,at FROM mcp_tool_calls`+where+`
 ), populations AS (
 SELECT '' AS role,'' AS tool,outcome,duration_ns,replayed,changed FROM filtered
 UNION ALL SELECT caller_role,tool,outcome,duration_ns,replayed,changed FROM filtered
 ), ranked AS (
 SELECT *,row_number() OVER (PARTITION BY role,tool ORDER BY duration_ns) AS rank,
  count(*) OVER (PARTITION BY role,tool) AS sample_size FROM populations
 ), totals AS (
 SELECT role,tool,-1 AS bucket,count(*) AS calls,sum(outcome<>'ok') AS failures,
  sum(replayed) AS replays,sum(changed) AS changes,sum(duration_ns) AS elapsed,
  max(CASE WHEN rank=sample_size-sample_size/20 THEN duration_ns END) AS p95
 FROM ranked GROUP BY role,tool
 )
 SELECT role,tool,bucket,calls,failures,replays,changes,elapsed,p95 FROM totals
 UNION ALL
 SELECT caller_role,'',(unixepoch(at)-?)/?,count(*),sum(outcome<>'ok'),0,0,sum(duration_ns),0
 FROM filtered GROUP BY 1,3
 ORDER BY 3,4 DESC,1,2`, append(args, from.Unix(), step)...)
	if err != nil {
		return result, storageError()
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row mcpToolMetrics
		var bucket int64
		if err := rows.Scan(&row.Role, &row.Tool, &bucket, &row.Count, &row.Failed, &row.Replayed, &row.Changed, &row.DurationNS, &row.P95NS); err != nil {
			return result, storageError()
		}
		switch {
		case bucket >= 0:
			result.Trend = append(result.Trend, mcpMetricBucket{
				At: from.Add(time.Duration(bucket*step) * time.Second), Role: row.Role,
				Count: row.Count, Failed: row.Failed, DurationNS: row.DurationNS,
			})
		case row.Role == "":
			result.Total = row.mcpMetricStats
		default:
			result.Tools = append(result.Tools, row)
		}
	}
	if rows.Err() != nil {
		return result, storageError()
	}
	return result, nil
}
