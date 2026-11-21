package core

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const (
	coreMetricSelector = `service_name="superagent-core"`
	processServices    = `service_name=~"massd|superagent-core"`
	mcpDurationMetric  = "superagent_mcp_tool_duration"
)

var metricFilterValue = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)

type metricRangeQuery struct {
	id, expression string
	labels         []string
}

func observerMetricQueries(q url.Values, lookback string) ([]metricRangeQuery, error) {
	for name, values := range q {
		if len(values) != 1 || values[0] == "" {
			return nil, problem("invalid_argument", "use one nonempty value per metrics parameter")
		}
		if !slices.Contains([]string{"metric", "window", "groupBy", "service", "instance", "role", "tool", "action", "outcome"}, name) {
			return nil, problem("invalid_argument", "unknown metrics parameter")
		}
	}
	switch q.Get("metric") {
	case "", "process", "cpu", "rss", "goroutines", "file_descriptors":
		return processMetricQueries(q, lookback)
	case "mcp_rate", "mcp_failure_rate", "mcp_p95":
		return mcpMetricQueries(q)
	default:
		return nil, problem("invalid_argument", "unknown metric")
	}
}

func processMetricQueries(q url.Values, lookback string) ([]metricRangeQuery, error) {
	if q.Has("role") || q.Has("tool") || q.Has("action") || q.Has("outcome") {
		return nil, problem("invalid_argument", "MCP filters require an MCP metric")
	}
	service, instance := q.Get("service"), q.Get("instance")
	if service != "" && service != "massd" && service != "superagent-core" {
		return nil, problem("invalid_argument", "unknown metrics service")
	}
	if instance != "" && !metricFilterValue.MatchString(instance) {
		return nil, problem("invalid_argument", "invalid metrics instance")
	}
	selector := []string{processServices, `service_instance_id!=""`}
	if service != "" {
		selector[0] = `service_name="` + service + `"`
	}
	if instance != "" {
		selector[1] = `service_instance_id="` + instance + `"`
	}
	var labels []string
	switch q.Get("groupBy") {
	case "", "instance":
		labels = []string{"service_name", "service_instance_id"}
	case "service":
		labels = []string{"service_name"}
	case "all":
	default:
		return nil, problem("invalid_argument", "invalid process metrics dimension")
	}
	selectors := "{" + strings.Join(selector, ",") + "}"
	group := metricGroup(labels)
	const perInstance = " by (service_name, service_instance_id) "
	gauge := func(metric string) string {
		expression := "max" + perInstance + "(last_over_time(" + metric + selectors + lookback + "))"
		if len(labels) < 2 {
			expression = "sum" + group + "(" + expression + ")"
		}
		return expression
	}
	queries := []metricRangeQuery{
		{"cpu", "sum" + group + "(rate(process_cpu_time" + selectors + "[5m]))", labels},
		{"rss", gauge("process_memory_usage"), labels},
		{"goroutines", gauge("go_goroutine_count"), labels},
		{"file_descriptors", gauge("process_unix_file_descriptor_count"), labels},
	}
	if id := q.Get("metric"); id != "" && id != "process" {
		queries = slices.DeleteFunc(queries, func(query metricRangeQuery) bool { return query.id != id })
	}
	return queries, nil
}

func mcpMetricQueries(q url.Values) ([]metricRangeQuery, error) {
	if q.Has("service") || q.Has("instance") {
		return nil, problem("invalid_argument", "service and instance filters require a process metric")
	}
	id := q.Get("metric")
	dimension := q.Get("groupBy")
	if dimension == "" {
		dimension = "role"
	}
	filters := []string{coreMetricSelector}
	var labels []string
	for _, name := range []string{"role", "tool", "action", "outcome"} {
		label := "superagent_" + name
		if dimension == name {
			labels = []string{label}
		}
		if value := q.Get(name); value != "" {
			if !metricFilterValue.MatchString(value) || (name == "outcome" && id == "mcp_failure_rate") {
				return nil, problem("invalid_argument", "invalid MCP metrics filter")
			}
			filters = append(filters, label+`="`+value+`"`)
		}
	}
	if dimension != "all" && (len(labels) == 0 || (dimension == "outcome" && id == "mcp_failure_rate")) {
		return nil, problem("invalid_argument", "invalid MCP metrics dimension")
	}
	group := metricGroup(labels)
	rate := func(suffix string, matchers []string) string {
		return "rate(" + mcpDurationMetric + suffix + "{" + strings.Join(matchers, ",") + "}[5m])"
	}
	total := "sum" + group + "(" + rate("_count", filters) + ")"
	expression := total
	switch id {
	case "mcp_failure_rate":
		failed := "sum" + group + "(" + rate("_count", append(filters, `superagent_outcome!="ok"`)) + ")"
		// Form 0% only for groups with observations. Divide by the rate of all outcomes.
		expression = "(" + failed + " or (0 * " + total + ")) / (" + total + " > 0)"
	case "mcp_p95":
		buckets := metricGroup(append([]string{"le"}, labels...))
		expression = "histogram_quantile(0.95, sum" + buckets + "(" + rate("_bucket", filters) + "))"
	}
	return []metricRangeQuery{{id, expression, labels}}, nil
}

func metricGroup(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	return " by (" + strings.Join(labels, ", ") + ") "
}
