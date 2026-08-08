package channels

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type IWebhookFormatter interface {
	Format(title, body string, color int) any
}

type discordFormatter struct{}

func (discordFormatter) Format(title, body string, color int) any {
	return map[string]any{
		"embeds": []map[string]any{
			{"title": title, "description": body, "color": color},
		},
	}
}

type slackFormatter struct{}

func (slackFormatter) Format(title, body string, _ int) any {
	return map[string]any{"text": "*" + title + "*\n" + body}
}

type genericFormatter struct{}

func (genericFormatter) Format(title, body string, _ int) any {
	return map[string]any{"title": title, "text": body}
}

func selectFormatter(webhookUrl string) IWebhookFormatter {
	if strings.Contains(webhookUrl, "discord.com/api/webhooks") {
		return discordFormatter{}
	}

	if strings.Contains(webhookUrl, "hooks.slack.com") {
		return slackFormatter{}
	}

	return genericFormatter{}
}

type WebhookChannel struct {
	Url       string
	Timeout   time.Duration
	Formatter IWebhookFormatter
}

func CreateWebhookChannel(webhookUrl string, timeout time.Duration) *WebhookChannel {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}

	return &WebhookChannel{
		Url:       webhookUrl,
		Timeout:   timeout,
		Formatter: selectFormatter(webhookUrl),
	}
}

// Formats for Discord, Slack, or a plain webhook. Redirects are refused on purpose so no bounces our token to somewhere funny.
func (channel *WebhookChannel) Send(title, body string, color int) error {
	formatter := channel.Formatter
	if formatter == nil {
		formatter = selectFormatter(channel.Url)
	}

	data, err := json.Marshal(formatter.Format(title, body, color))
	if err != nil {
		return err
	}

	timeout := channel.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Post(channel.Url, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return statusError{code: resp.StatusCode}
	}

	return nil
}

type statusError struct{ code int }

func (err statusError) Error() string {
	return "webhook returned HTTP " + strconv.Itoa(err.code) + " (alert dropped)"
}
