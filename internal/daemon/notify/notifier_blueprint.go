package notify

import (
	"errors"
	"time"
)

type NotifierBlueprint struct {
	webhook       string
	command       string
	channels      []IAlertChannel
	afterFailures int
	timeout       time.Duration
	log           func(string)
}

func CreateNotifierBlueprint(
	webhook, command string,
	afterFailures int,
	timeout time.Duration,
	log func(string),
	extraChannels ...IAlertChannel,
) (NotifierBlueprint, error) {
	if webhook == "" && command == "" && len(extraChannels) == 0 {
		return NotifierBlueprint{}, errors.New("notifier blueprint needs a webhook, command, or channel")
	}

	if timeout < 0 {
		timeout = 8 * time.Second
	}

	copiedChannels := append([]IAlertChannel(nil), extraChannels...)
	return NotifierBlueprint{
		webhook:       webhook,
		command:       command,
		channels:      copiedChannels,
		afterFailures: afterFailures,
		timeout:       timeout,
		log:           log,
	}, nil
}
