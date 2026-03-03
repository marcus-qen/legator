package alerts

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	ChannelTypeSlack     = "slack"
	ChannelTypeEmail     = "email"
	ChannelTypePagerDuty = "pagerduty"

	defaultPagerDutyEventsAPIURL = "https://events.pagerduty.com/v2/enqueue"
)

// NotificationChannel defines one first-class delivery destination for alert notifications.
type NotificationChannel struct {
	ID        string                  `json:"id"`
	Name      string                  `json:"name"`
	Type      string                  `json:"type"`
	Enabled   bool                    `json:"enabled"`
	Slack     *SlackChannelConfig     `json:"slack,omitempty"`
	Email     *EmailChannelConfig     `json:"email,omitempty"`
	PagerDuty *PagerDutyChannelConfig `json:"pagerduty,omitempty"`
	CreatedAt time.Time               `json:"created_at"`
	UpdatedAt time.Time               `json:"updated_at"`
}

// SlackChannelConfig stores Slack delivery settings.
type SlackChannelConfig struct {
	WebhookURL string `json:"webhook_url"`
	Channel    string `json:"channel,omitempty"`
}

// EmailChannelConfig stores SMTP/email delivery settings.
type EmailChannelConfig struct {
	SMTPHost string   `json:"smtp_host"`
	SMTPPort int      `json:"smtp_port"`
	Username string   `json:"username,omitempty"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

// PagerDutyChannelConfig stores PagerDuty Events API settings.
type PagerDutyChannelConfig struct {
	IntegrationKey string `json:"integration_key"`
	EventsAPIURL   string `json:"events_api_url,omitempty"`
}

type channelConfigEnvelope struct {
	Slack     *SlackChannelConfig     `json:"slack,omitempty"`
	Email     *EmailChannelConfig     `json:"email,omitempty"`
	PagerDuty *PagerDutyChannelConfig `json:"pagerduty,omitempty"`
}

func normalizeChannelInput(channel NotificationChannel) (NotificationChannel, error) {
	channel.Name = strings.TrimSpace(channel.Name)
	channel.Type = strings.ToLower(strings.TrimSpace(channel.Type))

	if channel.Name == "" {
		return channel, fmt.Errorf("name is required")
	}

	switch channel.Type {
	case ChannelTypeSlack:
		if channel.Slack == nil {
			channel.Slack = &SlackChannelConfig{}
		}
		channel.Slack.WebhookURL = strings.TrimSpace(channel.Slack.WebhookURL)
		channel.Slack.Channel = strings.TrimSpace(channel.Slack.Channel)
		if err := validateWebhookURL(channel.Slack.WebhookURL); err != nil {
			return channel, fmt.Errorf("invalid slack webhook_url: %w", err)
		}
		channel.Email = nil
		channel.PagerDuty = nil
	case ChannelTypeEmail:
		if channel.Email == nil {
			channel.Email = &EmailChannelConfig{}
		}
		channel.Email.SMTPHost = strings.TrimSpace(channel.Email.SMTPHost)
		channel.Email.Username = strings.TrimSpace(channel.Email.Username)
		channel.Email.From = strings.TrimSpace(channel.Email.From)
		cleanTo := make([]string, 0, len(channel.Email.To))
		for _, raw := range channel.Email.To {
			addr := strings.TrimSpace(raw)
			if addr == "" {
				continue
			}
			cleanTo = append(cleanTo, addr)
		}
		channel.Email.To = cleanTo
		if channel.Email.SMTPHost == "" {
			return channel, fmt.Errorf("email.smtp_host is required")
		}
		if channel.Email.SMTPPort <= 0 || channel.Email.SMTPPort > 65535 {
			return channel, fmt.Errorf("email.smtp_port must be between 1 and 65535")
		}
		if channel.Email.From == "" {
			return channel, fmt.Errorf("email.from is required")
		}
		if _, err := mail.ParseAddress(channel.Email.From); err != nil {
			return channel, fmt.Errorf("email.from is invalid")
		}
		if len(channel.Email.To) == 0 {
			return channel, fmt.Errorf("email.to must include at least one recipient")
		}
		for _, recipient := range channel.Email.To {
			if _, err := mail.ParseAddress(recipient); err != nil {
				return channel, fmt.Errorf("email.to contains invalid recipient: %s", recipient)
			}
		}
		channel.Slack = nil
		channel.PagerDuty = nil
	case ChannelTypePagerDuty:
		if channel.PagerDuty == nil {
			channel.PagerDuty = &PagerDutyChannelConfig{}
		}
		channel.PagerDuty.IntegrationKey = strings.TrimSpace(channel.PagerDuty.IntegrationKey)
		channel.PagerDuty.EventsAPIURL = strings.TrimSpace(channel.PagerDuty.EventsAPIURL)
		if channel.PagerDuty.IntegrationKey == "" {
			return channel, fmt.Errorf("pagerduty.integration_key is required")
		}
		if channel.PagerDuty.EventsAPIURL != "" {
			if err := validateWebhookURL(channel.PagerDuty.EventsAPIURL); err != nil {
				return channel, fmt.Errorf("invalid pagerduty.events_api_url: %w", err)
			}
		}
		channel.Slack = nil
		channel.Email = nil
	default:
		return channel, fmt.Errorf("unsupported channel type: %s", channel.Type)
	}

	return channel, nil
}

func validateWebhookURL(raw string) error {
	u, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if u.Host == "" {
		return fmt.Errorf("host is required")
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	return nil
}

func marshalChannelConfig(channel NotificationChannel) (string, error) {
	payload := channelConfigEnvelope{
		Slack:     channel.Slack,
		Email:     channel.Email,
		PagerDuty: channel.PagerDuty,
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(blob), nil
}

func (s *Store) CreateChannel(channel NotificationChannel) (*NotificationChannel, error) {
	now := time.Now().UTC()
	if channel.ID == "" {
		channel.ID = uuid.NewString()
	}
	if channel.CreatedAt.IsZero() {
		channel.CreatedAt = now
	}
	channel.UpdatedAt = now

	configJSON, err := marshalChannelConfig(channel)
	if err != nil {
		return nil, fmt.Errorf("marshal channel config: %w", err)
	}

	enabled := 0
	if channel.Enabled {
		enabled = 1
	}

	_, err = s.db.Exec(`INSERT INTO notification_channels (id, name, type, enabled, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		channel.ID,
		channel.Name,
		channel.Type,
		enabled,
		configJSON,
		channel.CreatedAt.Format(time.RFC3339Nano),
		channel.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("insert channel: %w", err)
	}

	copyChannel := channel
	return &copyChannel, nil
}

func (s *Store) UpdateChannel(channel NotificationChannel) (*NotificationChannel, error) {
	if strings.TrimSpace(channel.ID) == "" {
		return nil, fmt.Errorf("channel id required")
	}

	now := time.Now().UTC()
	channel.UpdatedAt = now

	configJSON, err := marshalChannelConfig(channel)
	if err != nil {
		return nil, fmt.Errorf("marshal channel config: %w", err)
	}

	enabled := 0
	if channel.Enabled {
		enabled = 1
	}

	result, err := s.db.Exec(`UPDATE notification_channels
		SET name = ?, type = ?, enabled = ?, config_json = ?, updated_at = ?
		WHERE id = ?`,
		channel.Name,
		channel.Type,
		enabled,
		configJSON,
		channel.UpdatedAt.Format(time.RFC3339Nano),
		channel.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update channel: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, sql.ErrNoRows
	}

	return s.GetChannel(channel.ID)
}

func (s *Store) GetChannel(id string) (*NotificationChannel, error) {
	row := s.db.QueryRow(`SELECT id, name, type, enabled, config_json, created_at, updated_at
		FROM notification_channels WHERE id = ?`, id)
	return scanChannel(row)
}

func (s *Store) ListChannels() ([]NotificationChannel, error) {
	rows, err := s.db.Query(`SELECT id, name, type, enabled, config_json, created_at, updated_at
		FROM notification_channels
		ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]NotificationChannel, 0)
	for rows.Next() {
		channel, err := scanChannel(rows)
		if err != nil {
			continue
		}
		out = append(out, *channel)
	}
	return out, rows.Err()
}

func (s *Store) DeleteChannel(id string) error {
	result, err := s.db.Exec(`DELETE FROM notification_channels WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func scanChannel(s scanner) (*NotificationChannel, error) {
	var (
		channel                             NotificationChannel
		enabled                             int
		configJSON, createdAt, updatedAtRaw string
	)

	if err := s.Scan(
		&channel.ID,
		&channel.Name,
		&channel.Type,
		&enabled,
		&configJSON,
		&createdAt,
		&updatedAtRaw,
	); err != nil {
		return nil, err
	}

	channel.Enabled = enabled == 1
	channel.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	channel.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAtRaw)

	if strings.TrimSpace(configJSON) != "" {
		var payload channelConfigEnvelope
		if err := json.Unmarshal([]byte(configJSON), &payload); err == nil {
			channel.Slack = payload.Slack
			channel.Email = payload.Email
			channel.PagerDuty = payload.PagerDuty
		}
	}

	return &channel, nil
}
