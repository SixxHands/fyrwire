package notify

type IAlertChannel interface {
	Send(title, body string, color int) error
}
