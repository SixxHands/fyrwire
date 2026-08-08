package channels

import (
	"bytes"
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

type CommandChannel struct {
	Command string
}

func CreateCommandChannel(command string) *CommandChannel {
	return &CommandChannel{Command: command}
}

func (channel *CommandChannel) Send(title, body string, _ int) error {
	commandText := strings.NewReplacer(
		"{title}", title,
		"{body}", strings.ReplaceAll(body, "\n", " "),
	).Replace(channel.Command)

	var process *exec.Cmd
	if runtime.GOOS == "windows" {
		process = exec.Command("cmd", "/C", commandText)
	} else {
		process = exec.Command("sh", "-c", commandText)
	}

	var stderr bytes.Buffer
	process.Stderr = &stderr
	if err := process.Run(); err != nil {
		message := "alert command failed: " + err.Error()
		if line := firstLine(stderr.String()); line != "" {
			message += ": " + line
		}
		return errors.New(message)
	}

	return nil
}

func firstLine(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}

	if len(text) > 200 {
		text = text[:200]
	}

	return strings.TrimSpace(text)
}
