package notify

import "github.com/sixxhands/fyrwire/internal/daemon/notify/channels"

type NotifierFactory struct{}

func (NotifierFactory) Create(blueprint NotifierBlueprint) *Notifier {
	list := make([]IAlertChannel, 0, len(blueprint.channels)+2)
	if blueprint.webhook != "" {
		list = append(list, channels.CreateWebhookChannel(blueprint.webhook, blueprint.timeout))
	}

	if blueprint.command != "" {
		list = append(list, channels.CreateCommandChannel(blueprint.command))
	}

	list = append(list, blueprint.channels...)

	return &Notifier{
		AfterFailures: blueprint.afterFailures,
		Log:           blueprint.log,
		channels:      list,
	}
}
