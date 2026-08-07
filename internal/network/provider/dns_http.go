package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sixxhands/fyrwire/internal/node/model"
)

func TargetOf(recordType, value string) string {
	fields := strings.Fields(value)
	switch recordType {
	case "SRV":
		if len(fields) >= 4 {
			return fields[3]
		}
	case "HTTPS", "SVCB":
		if len(fields) >= 2 {
			return fields[1]
		}
	case "A", "AAAA", "CNAME":
		if len(fields) >= 1 {
			return fields[0]
		}
	}

	return ""
}

func SameHost(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}

func CheckTarget(target string) error {
	trimmed := strings.TrimSuffix(strings.TrimSpace(target), ".")
	if trimmed == "" {
		return model.Errorf("SRV/HTTPS target is empty")
	}

	if isIpv4Literal(trimmed) {
		return model.Errorf(
			"%q is an IP address, but an SRV/HTTPS target must be a hostname (point node.a_name at a domain name)",
			target,
		)
	}

	if strings.Contains(trimmed, "_") {
		return model.Errorf(
			"%q contains an underscore, which is allowed in SRV names but not in target hostnames",
			target,
		)
	}

	return nil
}

func isIpv4Literal(text string) bool {
	parts := strings.Split(text, ".")
	if len(parts) != 4 {
		return false
	}

	for _, part := range parts {
		if part == "" || len(part) > 3 {
			return false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}

	return true
}

func StandardValue(recordType, value string) string {
	fields := strings.Fields(value)
	switch recordType {
	case "HTTPS", "SVCB":
		if len(fields) >= 2 {
			params := make([]string, 0, len(fields))
			for _, part := range fields[2:] {
				params = append(params, strings.ReplaceAll(part, `"`, ""))
			}
			sort.Strings(params)
			target := strings.ToLower(strings.TrimSuffix(fields[1], "."))
			return strings.Join(append([]string{fields[0], target}, params...), " ")
		}
	case "SRV":
		if len(fields) >= 4 {
			target := strings.ToLower(strings.TrimSuffix(fields[3], "."))
			return strings.Join([]string{fields[0], fields[1], fields[2], target}, " ")
		}
	}

	return strings.ToLower(strings.TrimSpace(value))
}

func SendHttp(
	client *http.Client,
	method, targetUrl string,
	body []byte,
	headers map[string]string,
) (int, []byte, http.Header, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, targetUrl, reader)
	if err != nil {
		return 0, nil, nil, model.Errorf("bad request: %v", err)
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, model.Errorf("unreachable: %v", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, resp.Header, nil
}

func RetryAfter(headers http.Header) time.Duration {
	if value := headers.Get("Retry-After"); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil {
			return time.Duration(seconds) * time.Second
		}
	}

	return 0
}

func JsonOr(data []byte, fallback string) string {
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		trimmed := strings.TrimSpace(string(data))
		if trimmed == "" {
			return fallback
		}
		return truncateText(trimmed, 200)
	}

	return truncateText(flattenJson(parsed, ""), 300)
}

func flattenJson(value any, prefix string) string {
	switch item := value.(type) {
	case string:
		if prefix != "" {
			return prefix + ": " + item
		}
		return item
	case []any:
		parts := make([]string, 0, len(item))
		for _, child := range item {
			parts = append(parts, flattenJson(child, prefix))
		}
		return strings.Join(parts, "; ")
	case map[string]any:
		parts := make([]string, 0, len(item))
		for key, child := range item {
			nextPrefix := key
			if key == "detail" || key == "message" {
				nextPrefix = prefix
			}
			parts = append(parts, flattenJson(child, nextPrefix))
		}
		sort.Strings(parts)
		return strings.Join(parts, "; ")
	}

	return fmt.Sprint(value)
}

func truncateText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}

	return text[:limit]
}
